package tapo

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for the Python helper: when
// FAKE_TAPO_HELPER is set it speaks the helper's protocol instead of running
// tests, so the client is exercised against a real child process and real pipes.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_TAPO_HELPER") != "" {
		fakeHelper()
		return
	}
	os.Exit(m.Run())
}

func fakeHelper() {
	// Like the real helper, requests are answered concurrently, so a command
	// sent while the login is still being handled can overtake it.
	var mu sync.Mutex
	on, authed := false, false
	sc := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req map[string]any
		_ = json.Unmarshal(sc.Bytes(), &req)
		go func() {
			rep := map[string]any{"id": req["id"], "ok": true, "result": map[string]any{}}
			if req["cmd"] == "auth" && req["username"] == "me@example.com" {
				time.Sleep(50 * time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			switch req["cmd"] {
			case "auth":
				if req["username"] != "me@example.com" {
					rep["ok"], rep["error"] = false, "login rejected"
					break
				}
				authed = true
			case "toggle":
				on = !on
				rep["result"] = map[string]any{"on": on, "watts": 12.5, "alias": "Desk"}
			case "set":
				on, _ = req["on"].(bool)
				rep["result"] = map[string]any{"on": on, "watts": nil, "alias": "Desk"}
			case "state":
				if req["host"] == "needs-login" && !authed {
					rep["ok"], rep["error"] = false, "login rejected"
					break
				}
				if req["host"] == "gone" {
					rep["ok"], rep["error"] = false, "no device answered at gone"
					break
				}
				rep["result"] = map[string]any{"on": on, "watts": 3.0, "alias": "Desk"}
			case "discover":
				rep["result"] = map[string]any{"plugs": []map[string]any{
					{"host": "10.0.0.5", "alias": "Desk", "model": "P110", "auth": true},
				}}
			case "die":
				os.Exit(1)
			}
			_ = out.Encode(rep)
		}()
	}
}

func fakeClient(t *testing.T) *Client {
	t.Helper()
	t.Setenv("FAKE_TAPO_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Client{Python: exe, Helper: exe}
}

func TestToggleAndStateRoundTrip(t *testing.T) {
	c := fakeClient(t)
	r, err := c.Toggle("10.0.0.5")
	if err != nil || !r.On || r.Watts == nil || *r.Watts != 12.5 || r.Alias != "Desk" {
		t.Fatalf("toggle = %+v, %v", r, err)
	}
	r, err = c.Set("10.0.0.5", false)
	if err != nil || r.On || r.Watts != nil {
		t.Fatalf("set = %+v, %v (a plug with no meter should have nil watts)", r, err)
	}
}

func TestHelperErrorsComeBackAsErrors(t *testing.T) {
	c := fakeClient(t)
	if _, err := c.State("gone"); err == nil || !strings.Contains(err.Error(), "no device answered") {
		t.Fatalf("err = %v", err)
	}
	// One failing plug must not take the helper down for the others.
	if _, err := c.State("10.0.0.5"); err != nil {
		t.Fatalf("next call: %v", err)
	}
}

func TestLoginIsSentBeforeTheFirstCommand(t *testing.T) {
	c := fakeClient(t)
	c.SetLogin("someone@else.com", "pw")
	// The fake rejects any other account; the failure is logged, not fatal, and
	// commands still go through (the plug will say if it needs the login).
	if _, err := c.State("10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	if !c.HasLogin() {
		t.Fatal("login not recorded")
	}
}

func TestDiscover(t *testing.T) {
	plugs, err := fakeClient(t).Discover()
	if err != nil || len(plugs) != 1 || plugs[0].Host != "10.0.0.5" || plugs[0].Model != "P110" {
		t.Fatalf("plugs = %+v, %v", plugs, err)
	}
}

func TestHelperIsRestartedAfterItDies(t *testing.T) {
	c := fakeClient(t)
	if _, err := c.State("10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(map[string]any{"cmd": "die"}, time.Second); err == nil {
		t.Fatal("expected the dying helper to fail its call")
	}
	if _, err := c.State("10.0.0.5"); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

func TestMissingSetupIsReportedAndNotRetriedHot(t *testing.T) {
	c := &Client{Python: "/nonexistent/python", Helper: "/nonexistent/helper.py"}
	_, err := c.State("h")
	if err != ErrNotSetUp {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.State("h"); err != ErrNotSetUp {
		t.Fatalf("second err = %v", err)
	}
	if c.p != nil {
		t.Fatal("started a process despite no setup")
	}
}

func TestCommandsThatRaceTheStartWaitForTheLogin(t *testing.T) {
	c := fakeClient(t)
	c.SetLogin("me@example.com", "pw")
	// The fake answers "state" for a plug at "needs-login" only once it has the
	// login, and takes its time over the login, so any command that overtakes
	// it fails.
	errs := make(chan error, 8)
	for i := 0; i < cap(errs); i++ {
		go func() {
			_, err := c.State("needs-login")
			errs <- err
		}()
	}
	for i := 0; i < cap(errs); i++ {
		if err := <-errs; err != nil {
			t.Fatalf("a command overtook the login: %v", err)
		}
	}
}
