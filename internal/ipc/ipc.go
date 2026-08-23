package ipc

import (
	"bufio"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const SockPath = "/tmp/lightwave.sock"

type Server struct {
	ln     net.Listener
	onCmd  func(string)
	mu     sync.Mutex
	closed bool
}

func DialOrServe(command string, onCmd func(string)) (primary bool, srv *Server, err error) {
	if conn, err := net.DialTimeout("unix", SockPath, 400*time.Millisecond); err == nil {
		_, _ = conn.Write([]byte(command + "\n"))
		_ = conn.Close()
		return false, nil, nil
	}
	_ = os.Remove(SockPath)

	ln, err := net.Listen("unix", SockPath)
	if err != nil {
		return false, nil, err
	}
	_ = os.Chmod(SockPath, 0o600)

	s := &Server{ln: ln, onCmd: onCmd}
	go s.accept()
	return true, s, nil
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
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.ToUpper(strings.TrimSpace(line))
	if cmd == "" {
		cmd = "TOGGLE"
	}
	if s.onCmd != nil {
		s.onCmd(cmd)
	}
}

func (s *Server) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
	_ = os.Remove(SockPath)
}
