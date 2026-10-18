package dreame

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for the Python helper, as in the tapo
// tests: a real child process and real pipes, no Python.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_DREAME_HELPER") != "" {
		fakeHelper()
		return
	}
	os.Exit(m.Run())
}

func fakeHelper() {
	cleaning := false
	sc := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	state := func() map[string]any {
		label := "DOCKED"
		if cleaning {
			label = "CLEANING"
		}
		return map[string]any{"label": label, "active": cleaning, "battery": 88}
	}
	for sc.Scan() {
		var req map[string]any
		_ = json.Unmarshal(sc.Bytes(), &req)
		rep := map[string]any{"id": req["id"], "ok": true, "result": map[string]any{}}
		switch req["cmd"] {
		case "auth":
			if req["country"] != "us" {
				rep["ok"], rep["error"] = false, "wrong region"
			}
		case "toggle":
			cleaning = !cleaning
			rep["result"] = state()
		case "room":
			if req["room"] != float64(3) || req["repeat"] != float64(2) {
				rep["ok"], rep["error"] = false, "unexpected room request"
				break
			}
			cleaning = true
			rep["result"] = state()
		case "state":
			if req["did"] == "gone" {
				rep["ok"], rep["error"] = false, "this account has no vacuum with id gone"
				break
			}
			rep["result"] = state()
		case "devices":
			rep["result"] = map[string]any{"vacuums": []map[string]any{
				{"did": "1", "name": "CC", "model": "dreame.vacuum.r2228o", "online": true},
			}}
		}
		_ = out.Encode(rep)
	}
}

func fakeClient(t *testing.T) *Client {
	t.Helper()
	t.Setenv("FAKE_DREAME_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Client{Python: exe, Helper: exe}
}

func TestToggleAndStateRoundTrip(t *testing.T) {
	c := fakeClient(t)
	s, err := c.Do("1", Toggle)
	if err != nil || s.Label != "CLEANING" || !s.Active || s.Battery == nil || *s.Battery != 88 {
		t.Fatalf("toggle = %+v, %v", s, err)
	}
	s, err = c.State("1")
	if err != nil || !s.Active {
		t.Fatalf("state = %+v, %v", s, err)
	}
}

func TestVacuums(t *testing.T) {
	vs, err := fakeClient(t).Vacuums()
	if err != nil || len(vs) != 1 || vs[0].Name != "CC" || vs[0].DID != "1" {
		t.Fatalf("vacuums = %+v, %v", vs, err)
	}
}

func TestHelperErrorsComeBackAsErrors(t *testing.T) {
	c := fakeClient(t)
	if _, err := c.State("gone"); err == nil || !strings.Contains(err.Error(), "no vacuum") {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.State("1"); err != nil {
		t.Fatalf("next call: %v", err)
	}
}

func TestLoginRegionDefaultsToUS(t *testing.T) {
	c := fakeClient(t)
	c.SetLogin("me@example.com", "pw", "")
	if !c.HasLogin() || c.country != "us" {
		t.Fatalf("login not recorded: %+v", c)
	}
	if _, err := c.State("1"); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSetupIsReported(t *testing.T) {
	c := &Client{Python: "/nonexistent/python", Helper: "/nonexistent/helper.py"}
	if _, err := c.State("1"); err != ErrNotSetUp {
		t.Fatalf("err = %v", err)
	}
	if c.p != nil {
		t.Fatal("started a process despite no setup")
	}
}

func TestDoRoomSendsTheRoomAndRepeat(t *testing.T) {
	t.Setenv("FAKE_DREAME_HELPER", "1")
	exe, _ := os.Executable()
	c := &Client{Python: exe, Helper: exe}
	// The fake answers any command; this checks the request is well formed by
	// way of the round trip succeeding with a state back.
	s, err := c.DoRoom("1", 3, 2)
	if err != nil || s.Label == "" {
		t.Fatalf("DoRoom = %+v, %v", s, err)
	}
}
