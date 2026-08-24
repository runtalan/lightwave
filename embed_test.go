package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"lightwave/internal/web"
)

// The phone server is fed the same embedded bundle the desktop window runs.
// internal/web tests the server against frontend/dist on disk; this one tests
// the copy that actually ships, so a broken //go:embed pattern or fs.Sub path
// fails the build rather than shipping a binary that serves a blank page.
func TestEmbeddedBundleServes(t *testing.T) {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}

	var called string
	s := web.New()
	if err := s.Start(web.Options{
		// Loopback only: a test must not open a port to the network.
		Addr:   "127.0.0.1:0",
		Assets: dist,
		Call: func(m string, _ []json.RawMessage) (any, error) {
			called = m
			return map[string]any{"paletteName": "Sage"}, nil
		},
	}); err != nil {
		t.Fatalf("start with embedded assets: %v", err)
	}
	defer s.Stop()

	page := fetch(t, "http://"+s.Addr()+"/")
	bridge := strings.Index(page, "_lw/bridge.js")
	module := strings.Index(page, `<script type="module"`)
	if bridge < 0 || module < 0 || bridge > module {
		t.Fatalf("bridge not injected before the module script:\n%s", page)
	}

	// The hashed bundle the page references must be reachable, or the phone
	// loads a shell with no app in it.
	i := strings.Index(page, `src="./assets/`)
	if i < 0 {
		t.Fatalf("no bundle reference in page:\n%s", page)
	}
	i += len(`src="./`)
	ref := page[i : i+strings.Index(page[i:], `"`)]
	resp, err := http.Get("http://" + s.Addr() + "/" + ref)
	if err != nil {
		t.Fatalf("bundle %s: %v", ref, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bundle %s: status %d", ref, resp.StatusCode)
	}

	if js := fetch(t, "http://"+s.Addr()+"/_lw/bridge.js"); !strings.Contains(js, "window.go") {
		t.Fatal("bridge.js does not install window.go")
	}

	// And a control call reaches the dispatcher.
	body := strings.NewReader(`{"method":"ToggleSlot","args":[2]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/_lw/call", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if called != "ToggleSlot" || !strings.Contains(string(out), "Sage") {
		t.Fatalf("call round trip: called=%q body=%s", called, out)
	}
}

// A remote client must not be able to reach anything beyond the lights. This
// pins the refusal at the dispatcher, where it is enforced, so the list cannot
// quietly widen.
func TestWebCallRefusesNonControlMethods(t *testing.T) {
	a := &App{}
	for _, m := range []string{
		"Quit", "HideHUD", "ShowHUD", "OpenConfig", "CloseConfig", "ToggleWindow",
		"SaveMappings", "CommitMappings", "AssignSlot", "MoveSlot", "RenameSlot",
		"FillRemaining", "SaveSettings", "SetConfigAPIKey", "Discover", "ScanLAN",
		"GetDevices", "StartWindowDrag", "SetWebEnabled", "SetWebConfig",
	} {
		if _, err := a.webCall(m, nil); err == nil {
			t.Errorf("webCall(%q) was allowed; it must be refused", m)
		}
	}
}

// The state sent to a browser must never carry the desktop's config flags or
// its settings: the shared bundle would render a Config screen whose every
// button is refused, and the settings carry filesystem paths.
func TestWebStateIsControlOnly(t *testing.T) {
	full := HUDState{
		NeedsSetup: true, SetupOpen: true, ConfigOpen: true, FirstRun: true,
		Brightness: 61, PaletteName: "Blush",
		Settings: SettingsView{
			ConfigPath: "/home/u/config.json", HasAPIKey: true,
			WebAddr: ":8787", WebURLs: []string{"http://10.0.0.2:8787"},
		},
	}
	got := webStateFrom(full)
	if got.NeedsSetup || got.SetupOpen || got.ConfigOpen || got.FirstRun {
		t.Fatalf("config flags leaked to the web state: %+v", got)
	}
	if !reflect.DeepEqual(got.Settings, SettingsView{}) {
		t.Fatalf("settings leaked to the web state: %+v", got.Settings)
	}
	// Everything a phone needs to work the lights must survive.
	if got.Brightness != 61 || got.PaletteName != "Blush" {
		t.Fatalf("control state was dropped: %+v", got)
	}
}

func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
