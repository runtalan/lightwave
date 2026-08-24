package web

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Boots the server against the REAL built bundle, not a fixture, to prove the
// bridge is injected into the actual index.html Vite produces.
func TestRealBundle(t *testing.T) {
	// frontend/dist is gitignored, so a clean checkout has nothing to serve.
	// Skip rather than fail: `npm run build` makes this test meaningful.
	if _, err := os.Stat("../../frontend/dist/index.html"); err != nil {
		t.Skip("frontend not built; run npm run build in frontend/")
	}
	dist := os.DirFS("../../frontend/dist")
	var seen []string
	s := New()
	if err := s.Start(Options{
		Addr:   "127.0.0.1:0",
		Assets: dist,
		Call: func(m string, a []json.RawMessage) (any, error) {
			seen = append(seen, m)
			return map[string]any{"brightness": 55, "paletteName": "Sage"}, nil
		},
	}); err != nil {
		t.Fatalf("start against real dist: %v", err)
	}
	defer s.Stop()

	page := get(t, "http://"+s.Addr()+"/")
	if !strings.Contains(page, "_lw/bridge.js") {
		t.Fatal("bridge not injected into the real index.html")
	}
	bi := strings.Index(page, "_lw/bridge.js")
	mi := strings.Index(page, `<script type="module"`)
	if bi > mi {
		t.Fatalf("bridge injected after the module script (%d > %d)", bi, mi)
	}

	// The real hashed bundle and CSS must be reachable at the paths the page
	// references, or the phone gets a blank screen.
	for _, ref := range []string{"./assets/", "index-"} {
		if !strings.Contains(page, ref) {
			t.Fatalf("page missing %q", ref)
		}
	}
	start := strings.Index(page, `src="./assets/`) + len(`src="./`)
	end := strings.Index(page[start:], `"`) + start
	bundle := page[start:end]
	resp, err := http.Get("http://" + s.Addr() + "/" + bundle)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("bundle %s not served: %v", bundle, err)
	}
	resp.Body.Close()

	// And a control call round-trips.
	res := post(t, "http://"+s.Addr()+"/_lw/call", `{"method":"ToggleSlot","args":[4]}`, "")
	if !strings.Contains(res, "Sage") || len(seen) != 1 || seen[0] != "ToggleSlot" {
		t.Fatalf("call round trip: seen=%v res=%s", seen, res)
	}
	t.Logf("real bundle served: %s", bundle)
}
