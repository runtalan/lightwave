package color

import "testing"

// spreadOf reports the max channel distance between any swatch and the mean,
// which is the "width" Spread is meant to scale.
func spreadOf(p Palette) int {
	if len(p.Colors) == 0 {
		return 0
	}
	var sr, sg, sb int
	for _, c := range p.Colors {
		sr += c.R
		sg += c.G
		sb += c.B
	}
	n := len(p.Colors)
	mr, mg, mb := sr/n, sg/n, sb/n
	worst := 0
	for _, c := range p.Colors {
		for _, d := range []int{c.R - mr, c.G - mg, c.B - mb} {
			if d < 0 {
				d = -d
			}
			if d > worst {
				worst = d
			}
		}
	}
	return worst
}

func TestSpreadIdentity(t *testing.T) {
	for _, p := range Palettes {
		got := p.Spread(1)
		for i := range p.Colors {
			if got.Colors[i] != p.Colors[i] {
				t.Fatalf("%s swatch %d changed at amount 1: %v vs %v",
					p.Name, i, got.Colors[i], p.Colors[i])
			}
		}
	}
}

// Zero drift collapses the palette to a single colour, which is what "held on
// one colour" in the UI promises.
func TestSpreadZeroCollapses(t *testing.T) {
	for _, p := range Palettes {
		got := p.Spread(0)
		first := got.Colors[0]
		for i, c := range got.Colors {
			if c.R != first.R || c.G != first.G || c.B != first.B {
				t.Fatalf("%s swatch %d = %v, want all equal to %v", p.Name, i, c, first)
			}
		}
	}
}

// The point of the setting: a narrow palette must actually get wider. Checked
// on the calm palettes, which are the ones that prompted this.
func TestSpreadWidensNarrowPalettes(t *testing.T) {
	for _, name := range []string{"Sage", "Blush", "Morning Haze", "Lavender Mist"} {
		i, ok := IndexOf(name)
		if !ok {
			t.Fatalf("palette %q not found", name)
		}
		p := Palettes[i]
		base := spreadOf(p)
		wide := spreadOf(p.Spread(2))
		if wide <= base {
			t.Fatalf("%s: spread at 200%% = %d, want more than %d", name, wide, base)
		}
		narrow := spreadOf(p.Spread(0.5))
		if narrow >= base {
			t.Fatalf("%s: spread at 50%% = %d, want less than %d", name, narrow, base)
		}
	}
}

// Channels must clamp, never wrap: a wrapped channel would swing the hue to
// something unrelated to the palette.
func TestSpreadClampsChannels(t *testing.T) {
	for _, p := range Palettes {
		for _, amt := range []float64{3, 10, 100} {
			for i, c := range p.Spread(amt).Colors {
				if c.R < 0 || c.R > 255 || c.G < 0 || c.G > 255 || c.B < 0 || c.B > 255 {
					t.Fatalf("%s swatch %d at %v out of range: %v", p.Name, i, amt, c)
				}
			}
		}
	}
}

// Kelvin is only meaningful when the swatch already had one — Lerp treats 0 as
// unset, and inventing a temperature would light white diodes unexpectedly.
func TestSpreadKelvinOnlyWhereSet(t *testing.T) {
	for _, p := range Palettes {
		for _, amt := range []float64{0, 0.5, 2, 3} {
			got := p.Spread(amt)
			for i, c := range p.Colors {
				g := got.Colors[i].Kelvin
				if c.Kelvin == 0 && g != 0 {
					t.Fatalf("%s swatch %d gained Kelvin %d at %v", p.Name, i, g, amt)
				}
				if c.Kelvin > 0 && (g < 1000 || g > 10000) {
					t.Fatalf("%s swatch %d Kelvin %d at %v is implausible", p.Name, i, g, amt)
				}
			}
		}
	}
}

func TestSpreadEmptyPalette(t *testing.T) {
	if got := (Palette{Name: "empty"}).Spread(2); len(got.Colors) != 0 {
		t.Fatalf("empty palette gained colours: %v", got.Colors)
	}
}
