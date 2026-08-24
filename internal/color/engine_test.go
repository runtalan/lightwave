package color

import "testing"

func TestGradientAt(t *testing.T) {
	p := Palettes[0]
	for _, n := range []int{1, 2, 8, 15} {
		if got := len(p.GradientAt(0, n)); got != n {
			t.Fatalf("n=%d: got %d colours", n, got)
		}
	}
	if p.GradientAt(0, 0) != nil {
		t.Fatal("zero-width gradient should be nil")
	}
	if (Palette{}).GradientAt(0, 4) != nil {
		t.Fatal("empty palette should yield no gradient")
	}
}

// A gradient must stay inside its theme: every sampled colour is a blend of
// two adjacent palette swatches, so each channel lies within the palette's
// own range. This is what keeps "gradient" from drifting off-theme.
func TestGradientStaysInPalette(t *testing.T) {
	for _, p := range Palettes {
		lo, hi := 255, 0
		for _, c := range p.Colors {
			for _, v := range []int{c.R, c.G, c.B} {
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
		}
		for _, c := range p.Gradient(0, 12) {
			for _, v := range []int{c.R, c.G, c.B} {
				if v < lo || v > hi {
					t.Fatalf("%s: channel %d outside palette range %d..%d", p.Name, v, lo, hi)
				}
			}
		}
	}
}

// Offsetting rotates the ramp rather than changing its content, which is what
// lets each strip in a pool show a different slice of the same theme.
func TestGradientOffsetRotates(t *testing.T) {
	p := Palettes[4] // Purples
	n := len(p.Colors)
	a := p.Gradient(0, n)
	b := p.Gradient(1, n)
	if len(a) != n || len(b) != n {
		t.Fatalf("lengths %d/%d, want %d", len(a), len(b), n)
	}
	if a[1] != b[0] {
		t.Fatalf("offset did not rotate: a[1]=%v b[0]=%v", a[1], b[0])
	}
	if a[0] == b[0] {
		t.Fatal("offset produced an identical ramp")
	}
}

// The tour loops, so the two ends of a strip meet instead of showing a seam.
func TestGradientLoops(t *testing.T) {
	p := Palettes[7] // Sunset
	g := p.GradientAt(0, 10)
	if g[0] != p.Walk(0, 0) {
		t.Fatal("gradient does not start at the tour origin")
	}
	if next := p.Walk(0, 1.0); g[0] != next {
		t.Fatalf("tour is not seamless: start=%v wrap=%v", g[0], next)
	}
}

// The palette table is written by hand, so guard the invariants the rest of
// the engine assumes: Distribute indexes up to five swatches, Walk needs at
// least two to interpolate between, and Lerp treats Kelvin 0 as "unset".
func TestPalettesWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, p := range Palettes {
		if p.Name == "" {
			t.Fatalf("palette %d has no name", i)
		}
		if seen[p.Name] {
			t.Fatalf("duplicate palette name %q", p.Name)
		}
		seen[p.Name] = true
		if len(p.Colors) < 2 {
			t.Fatalf("%s: %d swatches, need at least 2", p.Name, len(p.Colors))
		}
		for j, c := range p.Colors {
			for _, ch := range []int{c.R, c.G, c.B} {
				if ch < 0 || ch > 255 {
					t.Fatalf("%s swatch %d: channel %d outside 0-255", p.Name, j, ch)
				}
			}
			if c.Kelvin != 0 && (c.Kelvin < 1000 || c.Kelvin > 10000) {
				t.Fatalf("%s swatch %d: implausible Kelvin %d", p.Name, j, c.Kelvin)
			}
		}
	}
}

// Every palette must survive the paths the app drives it through, at every
// pool size a nine-pad deck can produce.
func TestEveryPaletteDistributesAndRamps(t *testing.T) {
	for _, p := range Palettes {
		e := Engine{}
		for e.Name() != p.Name {
			e.Cycle(1)
		}
		for n := 1; n <= 9; n++ {
			if got := len(e.Distribute(n)); got != n {
				t.Fatalf("%s: Distribute(%d) = %d colours", p.Name, n, got)
			}
			if got := len(p.Gradient(n, gradientBands)); got != gradientBands {
				t.Fatalf("%s: Gradient offset %d = %d bands", p.Name, n, got)
			}
		}
	}
}

// Mirrors govee.GradientBands; kept local so the colour package stays free of
// transport dependencies.
const gradientBands = 8
