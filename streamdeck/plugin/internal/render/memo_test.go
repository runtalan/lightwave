package render

import "testing"

// The cache must hand back exactly what drawing afresh would.
func TestCachedPicturesMatchFreshOnes(t *testing.T) {
	sw := []Swatch{{255, 80, 0}, {0, 120, 255}}
	cases := []struct {
		name          string
		cached, fresh func() (string, error)
	}{
		{"Level", func() (string, error) { return Level(42, true) }, func() (string, error) { return level(42, true) }},
		{"PaletteKey", func() (string, error) { return PaletteKey("Ocean", sw) }, func() (string, error) { return paletteKey("Ocean", sw) }},
		{"Indicator", func() (string, error) { return Indicator(Status{Palette: "Ocean", Swatches: sw, LightsOn: 2, Total: 3}) },
			func() (string, error) { return indicator(Status{Palette: "Ocean", Swatches: sw, LightsOn: 2, Total: 3}) }},
	}
	for _, c := range cases {
		first, err := c.cached()
		if err != nil {
			t.Fatal(err)
		}
		again, _ := c.cached()
		fresh, _ := c.fresh()
		if first != fresh || again != fresh {
			t.Fatalf("%s: cached picture differs from a fresh one", c.name)
		}
	}
}

// Different arguments must never share a picture.
func TestCacheKeysOnEveryArgument(t *testing.T) {
	a, _ := Level(42, true)
	b, _ := Level(42, false)
	c, _ := Level(43, true)
	if a == b || a == c {
		t.Fatal("pictures for different arguments came back the same")
	}
	w1, w2 := 12.5, 99.0
	p1, _ := PlugKey(Plug{Name: "Desk", On: true, Watts: &w1})
	p2, _ := PlugKey(Plug{Name: "Desk", On: true, Watts: &w2})
	if p1 == p2 {
		t.Fatal("plug keys with different watts came back the same")
	}
}
