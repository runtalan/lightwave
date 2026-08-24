package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<html><head><script type="module" crossorigin src="./assets/app.js"></script></head><body></body></html>`)},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
}

func startTest(t *testing.T, o Options) *Server {
	t.Helper()
	s := New()
	if o.Assets == nil {
		o.Assets = testAssets()
	}
	if o.Call == nil {
		o.Call = func(string, []json.RawMessage) (any, error) { return "ok", nil }
	}
	// Loopback only: the test must never open a port to the network.
	o.Addr = "127.0.0.1:0"
	if err := s.Start(o); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

// The private-network rule is the whole security boundary, so it is checked
// directly rather than only through the server.
func TestAllowedRemote(t *testing.T) {
	allowed := []string{
		"127.0.0.1:5000", "[::1]:5000",
		"10.0.0.4:5000", "172.16.9.1:5000", "172.31.255.254:5000", "192.168.1.50:5000",
		"100.92.4.7:5000",           // Tailscale / CGNAT
		"[fd7a:115c:a1e0::1]:5000",  // Tailscale IPv6 (ULA)
		"169.254.4.4:5000",          // link-local
		"[::ffff:192.168.1.9]:5000", // v4-mapped v6
	}
	for _, a := range allowed {
		if !AllowedRemote(a) {
			t.Errorf("AllowedRemote(%q) = false, want true", a)
		}
	}
	denied := []string{
		"8.8.8.8:5000", "1.1.1.1:443", "203.0.113.7:80",
		"[2606:4700:4700::1111]:443",
		"172.15.0.1:5000", "172.32.0.1:5000", // just outside 172.16/12
		"100.63.255.255:5000", "100.128.0.1:5000", // just outside 100.64/10
		"9.255.255.255:5000", "11.0.0.1:5000",
		"", "garbage", "not-an-ip:80",
	}
	for _, a := range denied {
		if AllowedRemote(a) {
			t.Errorf("AllowedRemote(%q) = true, want false", a)
		}
	}
}

func TestServesBridgedIndex(t *testing.T) {
	s := startTest(t, Options{})
	body := get(t, "http://"+s.Addr()+"/")
	// The bridge must be injected ahead of the module script, or the bundle
	// boots with no window.go and the page is dead.
	bi := strings.Index(body, "_lw/bridge.js")
	mi := strings.Index(body, `<script type="module"`)
	if bi < 0 || mi < 0 || bi > mi {
		t.Fatalf("bridge not injected before module script: %s", body)
	}
	if js := get(t, "http://"+s.Addr()+"/_lw/bridge.js"); !strings.Contains(js, "window.go") {
		t.Fatal("bridge.js does not install window.go")
	}
	if a := get(t, "http://"+s.Addr()+"/assets/app.js"); a != "console.log(1)" {
		t.Fatalf("static asset = %q", a)
	}
}

func TestCallDispatch(t *testing.T) {
	var gotMethod string
	var gotArgs []json.RawMessage
	s := startTest(t, Options{Call: func(m string, a []json.RawMessage) (any, error) {
		gotMethod, gotArgs = m, a
		return map[string]int{"brightness": 42}, nil
	}})
	res := post(t, "http://"+s.Addr()+"/_lw/call", `{"method":"ToggleSlot","args":[3]}`, "")
	if gotMethod != "ToggleSlot" || len(gotArgs) != 1 || string(gotArgs[0]) != "3" {
		t.Fatalf("dispatch got %q %v", gotMethod, gotArgs)
	}
	if !strings.Contains(res, `"brightness":42`) {
		t.Fatalf("result = %s", res)
	}
}

// A refused call must come back as a normal reply carrying an error, so the
// bridge can surface it instead of the browser treating it as a transport
// failure and retrying.
func TestCallErrorIsReported(t *testing.T) {
	s := startTest(t, Options{Call: func(string, []json.RawMessage) (any, error) {
		return nil, errNotAllowed{}
	}})
	res := post(t, "http://"+s.Addr()+"/_lw/call", `{"method":"Quit","args":[]}`, "")
	if !strings.Contains(res, "not available remotely") {
		t.Fatalf("expected refusal, got %s", res)
	}
}

type errNotAllowed struct{}

func (errNotAllowed) Error() string { return "Quit is not available remotely" }

func TestTokenRequiredWhenSet(t *testing.T) {
	s := startTest(t, Options{Token: "s3cret"})

	req, _ := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no token: status %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/", nil)
	req.Header.Set("X-Lightwave-Token", "s3cret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with token: status %d, want 200", resp.StatusCode)
	}

	// EventSource cannot set headers, so the query form must work too.
	resp, err = http.Get("http://" + s.Addr() + "/?token=s3cret")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query token: status %d, want 200", resp.StatusCode)
	}
}

func TestEventsStream(t *testing.T) {
	s := startTest(t, Options{})
	resp, err := http.Get("http://" + s.Addr() + "/_lw/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	// Publish until the subscriber has registered, then read one frame.
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		done <- string(buf[:n])
	}()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame := <-done:
			if !strings.Contains(frame, `"event":"state"`) || !strings.Contains(frame, `"brightness":7`) {
				t.Fatalf("frame = %q", frame)
			}
			return
		case <-deadline:
			t.Fatal("no SSE frame within 3s")
		default:
			s.Publish("state", map[string]int{"brightness": 7})
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// Toggling must be repeatable: a second Start without a Stop would leak a
// listener, and Stop on an idle server must not panic.
func TestStartStopCycle(t *testing.T) {
	s := startTest(t, Options{})
	if !s.Running() {
		t.Fatal("not running after start")
	}
	if err := s.Start(Options{Addr: "127.0.0.1:0", Assets: testAssets(),
		Call: func(string, []json.RawMessage) (any, error) { return nil, nil }}); err == nil {
		t.Fatal("double start should fail")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if s.Running() {
		t.Fatal("still running after stop")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if err := s.Start(Options{Addr: "127.0.0.1:0", Assets: testAssets(),
		Call: func(string, []json.RawMessage) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	_ = s.Stop()
}

func TestIndexWithoutModuleScriptIsRejected(t *testing.T) {
	s := New()
	err := s.Start(Options{
		Addr:   "127.0.0.1:0",
		Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}},
		Call:   func(string, []json.RawMessage) (any, error) { return nil, nil },
	})
	if err == nil {
		_ = s.Stop()
		t.Fatal("expected failure when index.html cannot be bridged")
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func post(t *testing.T, url, body, token string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Lightwave-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
