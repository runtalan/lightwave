package render

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var ocean = []Swatch{{0, 60, 120}, {0, 110, 170}, {20, 160, 200}, {90, 200, 220}, {170, 230, 240}}

// TestDialRenders draws every new key and strip. Set LIGHTWAVE_RENDER_DIR to
// keep the PNGs for a visual check.
func TestDialRenders(t *testing.T) {
	cases := map[string]func() (string, error){
		"mode-warm":    func() (string, error) { return ModeKey(ModeWarmness, ModePalette, ocean, 2700) },
		"mode-palette": func() (string, error) { return ModeKey(ModePalette, ModeSolid, ocean, 2700) },
		"mode-solid":   func() (string, error) { return ModeKey(ModeSolid, ModeWarmness, ocean, 2700) },
		"warm-key":     func() (string, error) { return WarmKey(2600, true) },
		"strip-swatch": func() (string, error) { return SwatchStrip(ocean) },
		"strip-solid":  func() (string, error) { return SolidStrip(ocean[2]) },
		"strip-warm":   func() (string, error) { return WarmStrip(3200) },
		"strip-mode":   func() (string, error) { return ModeStrip(ModePalette, ocean, 3200) },
	}
	dir := os.Getenv("LIGHTWAVE_RENDER_DIR")
	for name, f := range cases {
		uri, err := f()
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

func TestKelvinRGBWarmsAsKelvinFalls(t *testing.T) {
	warm, cool := KelvinRGB(MinKelvin), KelvinRGB(MaxKelvin)
	if warm.B >= cool.B || warm.G >= cool.G {
		t.Fatalf("%dK %v should be warmer than %dK %v", MinKelvin, warm, MaxKelvin, cool)
	}
}
