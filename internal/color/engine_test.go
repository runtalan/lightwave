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
