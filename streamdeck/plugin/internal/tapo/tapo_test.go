package tapo

import (
	"bufio"
	"encoding/json"
	"errors"
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
	if os.Getenv("FAKE_TAPO_HELPER") == "quit" {
		os.Exit(1) // as a helper with python-kasa missing does
	}
	// Like the real helper, requests are answered concurrently, and the login
	// takes a while, so a command sent alongside it can overtake it.
	var mu, outMu sync.Mutex
	on, user := false, ""
	sc := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req map[string]any
		_ = json.Unmarshal(sc.Bytes(), &req)
		go func() {
			rep := map[string]any{"id": req["id"], "ok": true, "result": map[string]any{}}
			switch req["cmd"] {
			case "auth":
				time.Sleep(50 * time.Millisecond)
				mu.Lock()
				user, _ = req["username"].(string)
				mu.Unlock()
				if req["username"] != "me@example.com" {
					rep["ok"], rep["error"] = false, "login rejected"
				}
			case "toggle":
				mu.Lock()
				on = !on
				rep["result"] = map[string]any{"on": on, "watts": 12.5, "alias": "Desk"}
				mu.Unlock()
			case "set":
				mu.Lock()
				on, _ = req["on"].(bool)
				rep["result"] = map[string]any{"on": on, "watts": nil, "alias": "Desk"}
				mu.Unlock()
			case "state":
				mu.Lock()
				cur, isOn := user, on
				mu.Unlock()
				switch {
				case req["host"] == "gone":
					rep["ok"], rep["error"] = false, "no device answered at gone"
				case req["host"] == "locked" && cur != "me@example.com":
					// A plug on current firmware: nothing without the right account.
					rep["ok"], rep["error"] = false, "login rejected"
				case req["host"] == "slow":
					time.Sleep(300 * time.Millisecond)
					rep["ok"], rep["error"] = false, "timed out"
				default:
					rep["result"] = map[string]any{"on": isOn, "watts": 3.0, "alias": "Desk"}
				}
			case "discover":
				rep["result"] = map[string]any{"plugs": []map[string]any{
					{"host": "10.0.0.5", "alias": "Desk", "model": "P110", "auth": true},
				}}
			case "die":
				os.Exit(1)
			}
			outMu.Lock()
			_ = out.Encode(rep)
			outMu.Unlock()
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

func TestNoCommandOvertakesTheLoginAtStartup(t *testing.T) {
	for i := 0; i < 20; i++ {
		c := fakeClient(t)
		c.SetLogin("me@example.com", "pw")
		// plugLoop reads every plug at once, so several first commands race
		// the helper's start.
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for n := 0; n < 4; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.State("locked"); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("run %d: a first command beat the login: %v", i, err)
		}
	}
}

func TestANewLoginIsUsedByTheVeryNextCommand(t *testing.T) {
	c := fakeClient(t)
	if _, err := c.State("locked"); err == nil {
		t.Fatal("the plug answered without a login")
	}
	// What applyGlobals does: set the login, then poll at once.
	c.SetLogin("me@example.com", "pw")
	if _, err := c.State("locked"); err != nil {
		t.Fatalf("the poll after the login change used the old one: %v", err)
	}
	c.SetLogin("", "")
	if _, err := c.State("locked"); err == nil {
		t.Fatal("a cleared login was not passed on")
	}
}

func TestAFailureDuringALoginChangeIsReportedAsStale(t *testing.T) {
	c := fakeClient(t)
	done := make(chan error, 1)
	go func() {
		_, err := c.State("slow")
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	c.SetLogin("me@example.com", "pw")
	if err := <-done; !errors.Is(err, ErrLoginChanged) {
		t.Fatalf("err = %v, want ErrLoginChanged", err)
	}
	// Without a change in between, a failure is just a failure.
	if _, err := c.State("slow"); err == nil || errors.Is(err, ErrLoginChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestAHelperThatQuitsAtStartIsNotRespawnedEveryPoll(t *testing.T) {
	c := fakeClient(t)
	t.Setenv("FAKE_TAPO_HELPER", "quit")
	_, err := c.State("h")
	if !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("first err = %v, want ErrNotSetUp so the key says NOT SET UP", err)
	}
	if _, err := c.State("h"); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("second err = %v", err)
	}
	if c.p != nil {
		t.Fatal("started the helper again inside the restart backoff")
	}
}
