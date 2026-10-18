package render

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVacuumKeyRenders draws each state. Set LIGHTWAVE_RENDER_DIR to keep the
// PNGs for a visual check.
func TestVacuumKeyRenders(t *testing.T) {
	cases := map[string]Vacuum{
		"vac-docked":       {Name: "CC", Label: "DOCKED", Battery: 100},
		"vac-cleaning":     {Name: "CC", Label: "CLEANING", Battery: 64, Active: true},
		"vac-returning":    {Name: "Living Room Vacuum", Label: "RETURNING", Battery: 18, Active: true},
		"vac-paused":       {Name: "CC", Label: "PAUSED", Battery: 52},
		"vac-nobattery":    {Name: "CC", Label: "IDLE", Battery: -1},
		"vac-offline":      {Name: "CC", Note: "OFFLINE"},
		"vac-nologin":      {Name: "Vacuum", Note: "NO LOGIN"},
		"vac-press-long":   {Name: "CC", Label: "DOCKED", Battery: 100, Press: "Primary Bedroom Closet Nook"},
		"vac-press-dock":   {Name: "CC", Label: "DOCKED", Battery: 100, Press: "DOCK"},
		"vac-press-room":   {Name: "CC", Label: "CLEANING", Battery: 64, Active: true, Press: "ROOM 12"},
		"vac-press-toggle": {Name: "CC", Label: "PAUSED", Battery: 52, Press: "START/PAUSE"},
	}
	dir := os.Getenv("LIGHTWAVE_RENDER_DIR")
	for name, v := range cases {
		uri, err := VacuumKey(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b64, ok := strings.CutPrefix(uri, "data:image/png;base64,")
		if !ok {
			t.Fatalf("%s: not a PNG data URI", name)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, name+".png"), raw, 0o644)
		}
	}
}
