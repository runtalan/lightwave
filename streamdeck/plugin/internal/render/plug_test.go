package render

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatWatts(t *testing.T) {
	for in, want := range map[float64]string{0: "0.0", 4.24: "4.2", 9.96: "10.0", 10: "10", 41.6: "42", 1250.4: "1250", -1: "0.0"} {
		if got := FormatWatts(in); got != want {
			t.Errorf("FormatWatts(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestPlugKeyRenders draws each state. Set LIGHTWAVE_RENDER_DIR to keep the
// PNGs for a visual check.
func TestPlugKeyRenders(t *testing.T) {
	w := func(f float64) *float64 { return &f }
	cases := map[string]Plug{
		"plug-on-small":   {Name: "Desk", On: true, Watts: w(4.2)},
		"plug-on":         {Name: "Office Heater", On: true, Watts: w(1250)},
		"plug-on-mid":     {Name: "Monitor", On: true, Watts: w(42.5)},
		"plug-on-nometer": {Name: "Lamp", On: true},
		"plug-off":        {Name: "Washing Machine", On: false},
		"plug-offline":    {Name: "Garage", Note: "OFFLINE"},
		"plug-nologin":    {Name: "Tapo Plug", Note: "NO LOGIN"},
		"plug-longname":   {Name: "Extraordinarilylongname", On: true, Watts: w(7)},
		"plug-empty":      {On: false},
	}
	dir := os.Getenv("LIGHTWAVE_RENDER_DIR")
	for name, p := range cases {
		uri, err := PlugKey(p)
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
