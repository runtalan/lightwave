// Package web serves the Lightwave HUD to a phone or tablet over a private
// network.
//
// It reuses the same embedded frontend bundle the desktop window runs, rather
// than shipping a second mobile UI. The bundle talks to Go through
// window.go.main.App and window.runtime, so bridge.js supplies both over HTTP
// before the bundle loads: control calls become POSTs and state pushes arrive
// on an SSE stream. The React code is byte-identical in both places.
//
// The server is off unless switched on, binds nothing until then, and refuses
// any client that is not on a private or VPN address.
package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// DefaultAddr is the listen address used when none is configured. Port 8787 is
// unregistered and unlikely to collide with a dev server.
const DefaultAddr = ":8787"

// CallFunc dispatches one control call from a browser. The app supplies it and
// owns the allowlist of what a remote client may invoke.
type CallFunc func(method string, args []json.RawMessage) (any, error)

// Options configures a run of the server.
type Options struct {
	// Addr is the listen address, e.g. ":8787" for every interface or
	// "100.92.4.7:8787" to bind only a VPN address.
	Addr string
	// Token, when set, must accompany every request. It is a second lock
	// behind the private-network check, for a VPN shared with people who
	// should not be touching the lights.
	Token string
	// Assets is the built frontend (the dist tree).
	Assets fs.FS
	// Call handles control calls.
	Call CallFunc
}

type Server struct {
	mu      sync.Mutex
	srv     *http.Server
	addr    string
	token   string
	assets  fs.FS
	index   []byte
	call    CallFunc
	subs    map[chan []byte]struct{}
	running bool
}

func New() *Server {
	return &Server{subs: map[chan []byte]struct{}{}}
}

// Start brings the server up. Calling it while already running is an error, so
// a toggle cannot leak listeners.
func (s *Server) Start(o Options) error {
	if o.Call == nil || o.Assets == nil {
		return errors.New("web: assets and call handler are required")
	}
	addr := strings.TrimSpace(o.Addr)
	if addr == "" {
		addr = DefaultAddr
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("web: already running")
	}
	index, err := buildIndex(o.Assets)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.assets, s.call, s.token, s.index = o.Assets, o.Call, strings.TrimSpace(o.Token), index
	s.mu.Unlock()

	// Listen before marking the server up, so a port clash surfaces as an
	// error the user sees rather than a toggle that silently does nothing.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web: listen %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_lw/call", s.handleCall)
	mux.HandleFunc("/_lw/events", s.handleEvents)
	mux.HandleFunc("/_lw/bridge.js", s.handleBridge)
	mux.HandleFunc("/", s.handleStatic)

	srv := &http.Server{
		Handler: s.guard(mux),
		// A phone that walks out of range must not hold a goroutine forever.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	s.mu.Lock()
	s.srv, s.addr, s.running = srv, ln.Addr().String(), true
	s.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("web: serve: %v", err)
		}
	}()
	log.Printf("web: control server on %s", ln.Addr())
	return nil
}

// Stop shuts the server down and drops every subscriber. Safe to call when
// already stopped.
func (s *Server) Stop() error {
	s.mu.Lock()
	srv, running := s.srv, s.running
	s.srv, s.running = nil, false
	subs := s.subs
	s.subs = map[chan []byte]struct{}{}
	s.mu.Unlock()
	for ch := range subs {
		close(ch)
	}
	if !running || srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Addr reports the bound address, which differs from the requested one when
// the port was left to the OS.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Publish pushes an event to every connected browser. Never blocks: a client
// that is not draining simply misses an update, which is right for state
// snapshots where only the newest matters.
func (s *Server) Publish(event string, data any) {
	s.mu.Lock()
	if !s.running || len(s.subs) == 0 {
		s.mu.Unlock()
		return
	}
	subs := make([]chan []byte, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	body, err := json.Marshal(struct {
		Event string `json:"event"`
		Data  any    `json:"data"`
	}{event, data})
	if err != nil {
		return
	}
	frame := append(append([]byte("data: "), body...), '\n', '\n')
	for _, ch := range subs {
		select {
		case ch <- frame:
		default:
		}
	}
}

// guard is the security boundary: every request must come from a private or
// VPN address, and carry the token when one is configured.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !AllowedRemote(r.RemoteAddr) {
			// Deliberately terse: a client that is not supposed to be here
			// learns nothing about what is running on this port.
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.mu.Lock()
		token := s.token
		s.mu.Unlock()
		if token != "" && !tokenOK(r, token) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenOK accepts the token from a header or the query string. EventSource
// cannot set headers, so the SSE stream needs the query form.
func tokenOK(r *http.Request, want string) bool {
	if got := r.Header.Get("X-Lightwave-Token"); got != "" {
		return subtleEqual(got, want)
	}
	return subtleEqual(r.URL.Query().Get("token"), want)
}

// subtleEqual compares in constant time, so a token cannot be recovered by
// timing repeated guesses from inside the VPN.
func subtleEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Method string            `json:"method"`
		Args   []json.RawMessage `json:"args"`
	}
	// A control payload is a method name and a few numbers; anything larger is
	// not something this API produces.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	s.mu.Lock()
	call := s.call
	s.mu.Unlock()
	if call == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not running"})
		return
	}
	result, err := call(req.Method, req.Args)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan []byte, 8)
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if _, still := s.subs[ch]; still {
			delete(s.subs, ch)
		}
		s.mu.Unlock()
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// A comment line every 25s keeps proxies and phone radios from dropping an
	// idle stream.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame, open := <-ch:
			if !open {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleBridge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "bridge.js", time.Time{}, bytes.NewReader(bridgeJS))
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	assets, index := s.assets, s.index
	s.mu.Unlock()
	if assets == nil {
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	clean := strings.TrimPrefix(r.URL.Path, "/")
	if clean == "" || clean == "index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
		return
	}
	http.FileServer(http.FS(assets)).ServeHTTP(w, r)
}

// buildIndex injects the bridge ahead of the app bundle. The bundle is a
// module and therefore deferred, so a classic script placed before it always
// runs first — which is what lets the same bundle boot against HTTP instead of
// the Wails runtime.
func buildIndex(assets fs.FS) ([]byte, error) {
	raw, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("web: read index.html: %w", err)
	}
	marker := []byte("<script type=\"module\"")
	i := bytes.Index(raw, marker)
	if i < 0 {
		return nil, errors.New("web: index.html has no module script to bridge")
	}
	tag := []byte("<script src=\"./_lw/bridge.js\"></script>\n    ")
	out := make([]byte, 0, len(raw)+len(tag))
	out = append(out, raw[:i]...)
	out = append(out, tag...)
	out = append(out, raw[i:]...)
	return out, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// AllowedRemote reports whether a client address may control the lights. Only
// loopback, RFC1918, link-local, IPv6 unique-local, and the 100.64/10 carrier
// range VPNs like Tailscale hand out are accepted. Everything else — every
// routable public address — is refused, so binding the server widely than
// intended still cannot expose the lights to the internet.
func AllowedRemote(remoteAddr string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	// Strip an IPv6 zone ("fe80::1%en0").
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if ip.Is4() {
		b := ip.As4()
		switch {
		case b[0] == 10:
			return true
		case b[0] == 172 && b[1] >= 16 && b[1] <= 31:
			return true
		case b[0] == 192 && b[1] == 168:
			return true
		case b[0] == 100 && b[1] >= 64 && b[1] <= 127:
			// 100.64/10, the range Tailscale and other VPNs allocate from.
			return true
		}
		return false
	}
	// fc00::/7, which covers Tailscale's IPv6 range.
	return ip.IsPrivate()
}

// URLs lists the addresses a phone can reach this server on, for display in
// Config. Only private addresses are listed, since no other address would be
// allowed to connect anyway.
func URLs(bound string) []string {
	_, port, err := net.SplitHostPort(bound)
	if err != nil {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			if ip.Is4In6() {
				ip = ip.Unmap()
			}
			if ip.IsLoopback() || !AllowedRemote(ip.String()) {
				continue
			}
			out = append(out, "http://"+net.JoinHostPort(ip.String(), port))
		}
	}
	return out
}
