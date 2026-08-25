package ipc

import (
	"bufio"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// SockPath is the single-instance / Stream Deck socket. macOS keeps the
// historical /tmp path so existing plugins keep working; Windows 11 speaks
// AF_UNIX on a filesystem path, so the rest of this file is shared.
func SockPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "lightwave.sock")
	}
	return "/tmp/lightwave.sock"
}

// Replier answers a command. Returning a non-empty string sends that line back
// to the caller; returning "" keeps the original fire-and-forget behaviour.
// Clients that send SUBSCRIBE are held open and fed state pushes instead.
type Replier func(cmd string) string

type Server struct {
	ln     net.Listener
	onCmd  func(string)
	reply  Replier
	mu     sync.Mutex
	closed bool
	subs   map[chan string]struct{}
}

func DialOrServe(command string, onCmd func(string)) (primary bool, srv *Server, err error) {
	path := SockPath()
	if conn, err := net.DialTimeout("unix", path, 400*time.Millisecond); err == nil {
		_, _ = conn.Write([]byte(command + "\n"))
		_ = conn.Close()
		return false, nil, nil
	}
	_ = os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return false, nil, err
	}
	_ = os.Chmod(path, 0o600)

	s := &Server{ln: ln, onCmd: onCmd, subs: map[chan string]struct{}{}}
	go s.accept()
	return true, s, nil
}

// SetReplier installs the handler used for request/response commands. Commands
// it does not recognise fall through to the fire-and-forget onCmd path, so the
// Stream Deck --toggle behaviour is unchanged.
func (s *Server) SetReplier(r Replier) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.reply = r
	s.mu.Unlock()
}

// Publish pushes a line to every subscribed client. Never blocks: a client that
// is not draining its channel simply misses the update, which is correct for
// state snapshots where only the newest matters.
func (s *Server) Publish(line string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	subs := make([]chan string, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- line:
		default:
		}
	}
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			log.Printf("ipc: accept: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	// A one-shot command must not hang the socket, but a subscriber stays for
	// the life of the app, so the deadline is only applied to the first read.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.ToUpper(strings.TrimSpace(line))
	if cmd == "" {
		cmd = "TOGGLE"
	}

	if cmd == "SUBSCRIBE" {
		s.serveSubscriber(conn, r)
		return
	}

	s.mu.Lock()
	reply := s.reply
	s.mu.Unlock()
	if reply != nil {
		if out := reply(cmd); out != "" {
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_, _ = conn.Write([]byte(out + "\n"))
			return
		}
	}
	if s.onCmd != nil {
		s.onCmd(cmd)
	}
}

// serveSubscriber keeps a client connected, streaming state lines until it
// disconnects or the app shuts down.
func (s *Server) serveSubscriber(conn net.Conn, r *bufio.Reader) {
	ch := make(chan string, 4)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.subs[ch] = struct{}{}
	reply := s.reply
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	// Send the current state immediately so a freshly connected plugin can
	// paint its keys without waiting for the next change.
	if reply != nil {
		if out := reply("STATE"); out != "" {
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := conn.Write([]byte(out + "\n")); err != nil {
				return
			}
		}
	}

	// A subscriber may also issue commands on the same connection.
	cmds := make(chan string, 8)
	go func() {
		defer close(cmds)
		for {
			conn.SetReadDeadline(time.Time{}) // no deadline while subscribed
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if c := strings.ToUpper(strings.TrimSpace(line)); c != "" {
				cmds <- c
			}
		}
	}()

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := conn.Write([]byte(line + "\n")); err != nil {
				return
			}
		case c, ok := <-cmds:
			if !ok {
				return
			}
			s.mu.Lock()
			rp := s.reply
			s.mu.Unlock()
			if rp != nil {
				if out := rp(c); out != "" {
					_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
					if _, err := conn.Write([]byte(out + "\n")); err != nil {
						return
					}
					continue
				}
			}
			if s.onCmd != nil {
				s.onCmd(c)
			}
		}
	}
}

func (s *Server) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	subs := s.subs
	s.subs = map[chan string]struct{}{}
	s.mu.Unlock()
	for ch := range subs {
		close(ch)
	}
	_ = s.ln.Close()
	_ = os.Remove(SockPath())
}
