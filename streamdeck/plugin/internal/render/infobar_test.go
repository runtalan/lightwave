package render

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInfobarRenders draws each state. Set LIGHTWAVE_RENDER_DIR to keep the
// PNGs for a visual check.
func TestInfobarRenders(t *testing.T) {
	cases := map[string]Infobar{
		"bar-cleaning": {Label: "CLEANING", Battery: 64, Minutes: 12, Area: 8, Active: true},
		"bar-room":     {Label: "CLEANING", Room: "KITCHEN", Battery: 64, Minutes: 12, Area: 8, Active: true},
		"bar-docked":   {Label: "CHARGING", Battery: 96, Minutes: 5, Area: 3},
		"bar-longroom": {Label: "RETURNING", Room: "PRIMARY BEDROOM", Battery: 18, Minutes: 41, Area: 33, Active: true},
		"bar-error":    {Label: "ERROR", Battery: 50, Minutes: 2, Area: 1, Error: 12},
		"bar-nodata":   {Label: "IDLE", Battery: -1, Minutes: -1, Area: -1},
		"bar-offline":  {Note: "OFFLINE"},
		"bar-nologin":  {Note: "NO LOGIN"},
	}
	dir := os.Getenv("LIGHTWAVE_RENDER_DIR")
	for name, v := range cases {
		uri, err := InfobarImage(v)
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

func TestRunText(t *testing.T) {
	for in, want := range map[Infobar]string{
		{Minutes: 12, Area: 8}:  "12 MIN 8 M2",
		{Minutes: 5, Area: -1}:  "5 MIN",
		{Minutes: -1, Area: 3}:  "3 M2",
		{Minutes: -1, Area: -1}: "",
	} {
		if got := runText(in); got != want {
			t.Errorf("runText(%+v) = %q, want %q", in, got, want)
		}
	}
}
