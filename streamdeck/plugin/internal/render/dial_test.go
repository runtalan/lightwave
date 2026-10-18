package render

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
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
		"strip-scene":  func() (string, error) { return PositionStrip(30, 110) },
		"strip-none":   func() (string, error) { return PositionStrip(0, 0) },
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

// A scene's icon lands on the right of the panel, scaled to fit, and the
// position track stops short of it.
func TestSceneStripPlacesTheIcon(t *testing.T) {
	icon := image.NewRGBA(image.Rect(0, 0, 222, 219))
	draw.Draw(icon, icon.Bounds(), &image.Uniform{color.RGBA{255, 0, 0, 255}}, image.Point{}, draw.Src)
	uri, err := SceneStrip(icon, 1, 90)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mid := sceneIcon.Min.Add(sceneIcon.Size().Div(2))
	if r, _, _, a := img.At(mid.X, mid.Y).RGBA(); r>>8 != 255 || a>>8 != 255 {
		t.Fatalf("icon centre = %v, want opaque red", img.At(mid.X, mid.Y))
	}
	if _, _, _, a := img.At(sceneIcon.Min.X-10, band.Min.Y+band.Dy()/2).RGBA(); a != 0 {
		t.Fatal("the track must stop short of the icon")
	}
	if _, err := SceneStrip(nil, 1, 90); err != nil {
		t.Fatal(err)
	}
}
