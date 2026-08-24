package color

type RGBK struct {
	R      int `json:"r"`
	G      int `json:"g"`
	B      int `json:"b"`
	Kelvin int `json:"kelvin"`
}

type Palette struct {
	Name   string
	Colors []RGBK
}

var Palettes = []Palette{
	{
		Name: "Warm Whites",
		Colors: []RGBK{
			{R: 255, G: 167, B: 87, Kelvin: 2700},
			{R: 255, G: 174, B: 109, Kelvin: 2850},
			{R: 255, G: 180, B: 130, Kelvin: 3000},
			{R: 255, G: 186, B: 142, Kelvin: 3100},
			{R: 255, G: 193, B: 155, Kelvin: 3200},
		},
	},
	{
		Name: "Soft Ambers",
		Colors: []RGBK{
			{R: 255, G: 196, B: 140, Kelvin: 2400},
			{R: 255, G: 168, B: 92},
			{R: 232, G: 140, B: 64},
			{R: 210, G: 118, B: 48},
			{R: 255, G: 214, B: 170},
		},
	},
	{
		Name: "Deep Oranges",
		Colors: []RGBK{
			{R: 255, G: 92, B: 18},
			{R: 255, G: 122, B: 24},
			{R: 224, G: 64, B: 12},
			{R: 255, G: 154, B: 46},
			{R: 186, G: 42, B: 8},
		},
	},
	{
		Name: "Reds",
		Colors: []RGBK{
			{R: 255, G: 28, B: 36},
			{R: 220, G: 8, B: 32},
			{R: 176, G: 0, B: 28},
			{R: 255, G: 64, B: 72},
			{R: 140, G: 0, B: 42},
		},
	},
	{
		Name: "Purples",
		Colors: []RGBK{
			{R: 176, G: 38, B: 255},
			{R: 132, G: 24, B: 220},
			{R: 255, G: 46, B: 200},
			{R: 92, G: 16, B: 196},
			{R: 210, G: 90, B: 255},
		},
	},
	{
		// Teal through deep sea blue: an analogous run around cyan, so two
		// lamps read as related water tones rather than one flat colour.
		Name: "Ocean",
		Colors: []RGBK{
			{R: 64, G: 224, B: 208},
			{R: 32, G: 178, B: 190},
			{R: 16, G: 132, B: 178},
			{R: 10, G: 92, B: 158},
			{R: 120, G: 240, B: 224},
		},
	},
	{
		// Turning-leaf colours: russet and ochre against a deep maple red.
		Name: "Fall Leaves",
		Colors: []RGBK{
			{R: 214, G: 92, B: 24},
			{R: 236, G: 148, B: 38},
			{R: 168, G: 52, B: 20},
			{R: 226, G: 186, B: 74},
			{R: 130, G: 34, B: 26},
		},
	},
	{
		// Dusk gradient: hot horizon orange climbing into violet sky, which
		// gives pooled lamps a warm/cool split instead of near-identical hues.
		Name: "Sunset",
		Colors: []RGBK{
			{R: 255, G: 138, B: 48},
			{R: 255, G: 94, B: 88},
			{R: 232, G: 62, B: 132},
			{R: 158, G: 54, B: 166},
			{R: 96, G: 52, B: 168},
		},
	},
}

type Engine struct {
	Index int
}

func (e *Engine) Palette() Palette {
	if e.Index < 0 {
		e.Index = 0
	}
	e.Index = e.Index % len(Palettes)
	return Palettes[e.Index]
}

func (e *Engine) Cycle(dir int) Palette {
	n := len(Palettes)
	e.Index = (e.Index + dir) % n
	if e.Index < 0 {
		e.Index += n
	}
	return e.Palette()
}

func (e *Engine) Name() string {
	return e.Palette().Name
}

// Distribute spreads complementary/adjacent colors from the current palette
// across n devices. A single device gets the palette's center swatch.
func (e *Engine) Distribute(n int) []RGBK {
	p := e.Palette()
	cols := p.Colors
	if n <= 0 {
		return nil
	}
	if n == 1 || len(cols) == 1 {
		return []RGBK{cols[len(cols)/2]}
	}
	out := make([]RGBK, n)
	last := len(cols) - 1
	used := make([]bool, len(cols))
	idxOf := make([]int, n)
	for i := 0; i < n; i++ {
		idx := int(float64(i) * float64(last) / float64(n-1))
		idxOf[i] = idx
		out[i] = cols[idx]
		used[idx] = true
	}
	// Give the middle lamp a contrasting swatch, but never one already in use:
	// two lamps showing the identical colour is exactly what this is meant to
	// avoid. If every swatch is taken (n >= len(cols)) leave the spread as is.
	if n >= 3 && last >= 2 {
		mid := n / 2
		start := (e.Index*3 + 2) % len(cols)
		for off := 0; off < len(cols); off++ {
			cand := (start + off) % len(cols)
			if !used[cand] {
				used[idxOf[mid]] = false
				out[mid] = cols[cand]
				used[cand] = true
				break
			}
		}
	}
	return out
}

// Lerp blends two colours. t is clamped to 0..1.
func Lerp(a, b RGBK, t float64) RGBK {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	mix := func(x, y int) int {
		return int(float64(x) + (float64(y)-float64(x))*t + 0.5)
	}
	out := RGBK{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B)}
	// Kelvin only carries meaning when both endpoints specify it; blending a
	// real temperature with 0 would drag the colour toward an invalid value.
	if a.Kelvin > 0 && b.Kelvin > 0 {
		out.Kelvin = mix(a.Kelvin, b.Kelvin)
	}
	return out
}

// Walk returns the colour at position t (0..1) along a looped tour of the
// palette, starting from swatch `offset`. Giving each lamp a different offset
// keeps them in related but distinct parts of the palette as they drift.
func (p Palette) Walk(offset int, t float64) RGBK {
	n := len(p.Colors)
	if n == 0 {
		return RGBK{}
	}
	if n == 1 {
		return p.Colors[0]
	}
	// Wrap into 0..1 so callers can pass a freely increasing phase.
	t = t - float64(int(t))
	if t < 0 {
		t += 1
	}
	pos := t * float64(n)
	i := int(pos)
	frac := pos - float64(i)
	a := p.Colors[((offset+i)%n+n)%n]
	b := p.Colors[((offset+i+1)%n+n)%n]
	return Lerp(a, b, frac)
}

// GradientAt samples n colours along a looped tour of the palette beginning at
// phase t (0..1). It is the multi-colour counterpart to Walk: where Walk gives
// one lamp one colour, this fills a strip's zones with a themed ramp. The tour
// is a loop rather than a first-to-last ramp, so the two ends of the strip meet
// on the same colour instead of showing a seam.
func (p Palette) GradientAt(t float64, n int) []RGBK {
	if n <= 0 || len(p.Colors) == 0 {
		return nil
	}
	out := make([]RGBK, n)
	for i := 0; i < n; i++ {
		// Walk wraps its phase, so a fraction of the tour per zone keeps the
		// spread even no matter how many zones are asked for.
		out[i] = p.Walk(0, t+float64(i)/float64(n))
	}
	return out
}

// Gradient samples n colours starting from swatch `offset`. Giving each strip
// in the pool a different offset keeps a room composed — related ramps out of
// the same palette rather than the identical gradient repeated.
func (p Palette) Gradient(offset, n int) []RGBK {
	if len(p.Colors) == 0 {
		return nil
	}
	return p.GradientAt(float64(offset)/float64(len(p.Colors)), n)
}
