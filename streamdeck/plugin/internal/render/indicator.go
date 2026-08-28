// Package render draws the Lightwave status key.
//
// Stream Deck keys accept a base64 PNG via setImage, so the indicator is drawn
// from scratch each update rather than switching between fixed state images.
// That lets it show live data: the palette's actual colours, its name, whether
// the colour fade is running, and how many lights are lit.
package render

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
)

// Size is the key resolution. 144 is the @2x size Stream Deck expects; it
// downscales cleanly to 72 on non-retina hardware.
const Size = 144

// Swatch is one palette colour.
type Swatch struct{ R, G, B int }

// Status is everything the indicator draws.
type Status struct {
	Palette    string   // palette name, e.g. "Ocean"
	Swatches   []Swatch // the palette's colours, left to right
	Dancing    bool     // colour fade running
	Gradient   bool     // gradient spread across devices vs one shared colour
	Brightness int      // pool level, 0..100
	LightsOn   int      // how many lights are lit
	Total      int      // how many lights are bound
	OnNames    []string // names of the lit lights, so one lit lamp can be named
	Phase      float64  // 0..1, advances over time to animate the wave
}

var (
	bg      = color.RGBA{6, 3, 12, 255}
	neon    = color.RGBA{0xb0, 0x26, 0xff, 255}
	magenta = color.RGBA{0xff, 0x2e, 0xc8, 255}
	ice     = color.RGBA{0xe9, 0xd5, 0xff, 255}
	dim     = color.RGBA{0x6a, 0x4a, 0x8a, 255}
)

func lerp(a, b color.RGBA, t float64) color.RGBA {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	f := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

func blend(dst, src color.RGBA, a float64) color.RGBA {
	if a <= 0 {
		return dst
	}
	if a > 1 {
		a = 1
	}
	f := func(d, s uint8) uint8 { return uint8(float64(d)*(1-a) + float64(s)*a) }
	return color.RGBA{f(dst.R, src.R), f(dst.G, src.G), f(dst.B, src.B), 255}
}

// Indicator renders the status key as a base64 PNG ready for setImage.
func Indicator(st Status) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))

	// Background: near-black with a faint purple grid, matching the app.
	for y := 0; y < Size; y++ {
		for x := 0; x < Size; x++ {
			c := bg
			// Subtle vertical falloff so the key has depth.
			c = blend(c, color.RGBA{18, 8, 32, 255}, 0.5*(1-float64(y)/Size))
			const cell = 24
			gx := math.Abs(math.Mod(float64(x), cell) - cell/2)
			gy := math.Abs(math.Mod(float64(y), cell) - cell/2)
			if gx > cell/2-1.2 || gy > cell/2-1.2 {
				c = blend(c, neon, 0.10)
			}
			img.Set(x, y, c)
		}
	}

	drawWave(img, st)
	drawHeadline(img, st)
	drawSubline(img, st)
	drawSwatches(img, st)
	drawPaletteName(img, st)
	drawFadeDot(img, st)
	drawPattern(img, st)
	drawFader(img, st)
	border(img)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// drawWave paints the neon wave across the middle of the key. The wave's phase
// advances between updates, so a still key gently drifts.
func drawWave(img *image.RGBA, st Status) {
	const midY = 20.0
	for x := 0; x < Size; x++ {
		u := float64(x) / Size
		for _, w := range []struct {
			amp, freq, off, weight, alpha float64
		}{
			{7, 2.1, 0.0, 2.0, 0.85},
			{5, 3.0, 1.7, 1.4, 0.45},
			{9, 1.4, 3.1, 1.1, 0.28},
		} {
			phase := st.Phase * 2 * math.Pi
			y := midY + w.amp*math.Sin(u*w.freq*2*math.Pi+w.off+phase)
			col := lerp(neon, magenta, u)
			// Anti-aliased stroke: fade out over ~1px past the line's half-width.
			lo, hi := int(y-w.weight-2), int(y+w.weight+2)
			for py := lo; py <= hi; py++ {
				if py < 0 || py >= Size {
					continue
				}
				d := math.Abs(float64(py) - y)
				a := 1.0 - (d-w.weight)/1.6
				if d <= w.weight {
					a = 1
				}
				if a <= 0 {
					continue
				}
				img.Set(x, py, blend(img.RGBAAt(x, py), col, a*w.alpha))
			}
		}
	}
}

// drawSwatches paints the palette's actual colours as a strip near the bottom.
func drawSwatches(img *image.RGBA, st Status) {
	if len(st.Swatches) == 0 {
		return
	}
	const top, h = 100, 10
	n := len(st.Swatches)
	w := float64(Size-16) / float64(n)
	for i, s := range st.Swatches {
		c := color.RGBA{uint8(s.R), uint8(s.G), uint8(s.B), 255}
		x0 := 8 + int(float64(i)*w)
		x1 := 8 + int(float64(i+1)*w)
		for x := x0; x < x1 && x < Size-8; x++ {
			for y := top; y < top+h; y++ {
				// Round the strip's outer corners.
				edge := 0.0
				if i == 0 && x-x0 < 3 {
					edge = float64(3-(x-x0)) / 3
				}
				if i == n-1 && x1-x < 3 {
					edge = float64(3-(x1-x)) / 3
				}
				cy := math.Abs(float64(y) - (top + h/2.0))
				if cy > h/2-1 && edge > 0.5 {
					continue
				}
				img.Set(x, y, c)
			}
		}
	}
	// Thin highlight along the top of the strip.
	for x := 8; x < Size-8; x++ {
		img.Set(x, top, blend(img.RGBAAt(x, top), color.RGBA{255, 255, 255, 255}, 0.18))
	}
}

// drawFadeDot shows whether the colour fade is running: a filled pulsing dot
// when live, a hollow ring when idle.
func drawFadeDot(img *image.RGBA, st Status) {
	cx, cy := 130.0, 14.0
	r := 5.0
	if st.Dancing {
		// Pulse with the same phase that drives the wave.
		r += 1.6 * math.Sin(st.Phase*2*math.Pi*2)
	}
	for y := int(cy - r - 4); y <= int(cy+r+4); y++ {
		for x := int(cx - r - 4); x <= int(cx+r+4); x++ {
			if x < 0 || y < 0 || x >= Size || y >= Size {
				continue
			}
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			col := lerp(magenta, neon, 0.3)
			if st.Dancing {
				if a := 1.0 - (d-r)/1.5; a > 0 {
					if d <= r {
						a = 1
					}
					img.Set(x, y, blend(img.RGBAAt(x, y), col, a))
				}
				if g := 1.0 - (d-r)/9.0; g > 0 && d > r {
					img.Set(x, y, blend(img.RGBAAt(x, y), col, g*0.35))
				}
			} else {
				if a := 1.0 - math.Abs(d-r)/1.4; a > 0 {
					img.Set(x, y, blend(img.RGBAAt(x, y), dim, a*0.9))
				}
			}
		}
	}
}

func border(img *image.RGBA) {
	for i := 0; i < Size; i++ {
		for _, p := range [][2]int{{i, 0}, {i, Size - 1}, {0, i}, {Size - 1, i}} {
			img.Set(p[0], p[1], blend(img.RGBAAt(p[0], p[1]), neon, 0.5))
		}
	}
}

// drawText renders a string in the 5x7 font at (x, y), scaled by `scale`.
// Returns the width drawn. Unknown runes are skipped.
func drawText(img *image.RGBA, s string, x, y, scale int, col color.RGBA, alpha float64) int {
	cx := x
	for _, r := range s {
		g, ok := font5x7[r]
		if !ok {
			if r >= 'a' && r <= 'z' {
				g, ok = font5x7[r-32] // fold to uppercase
			}
			if !ok {
				cx += (5 + 1) * scale
				continue
			}
		}
		for row := 0; row < 7; row++ {
			bits := g[row]
			for colIdx := 0; colIdx < 5; colIdx++ {
				if bits&(1<<(4-colIdx)) == 0 {
					continue
				}
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						px, py := cx+colIdx*scale+sx, y+row*scale+sy
						if px < 0 || py < 0 || px >= Size || py >= Size {
							continue
						}
						img.Set(px, py, blend(img.RGBAAt(px, py), col, alpha))
					}
				}
			}
		}
		cx += (5 + 1) * scale
	}
	return cx - x
}

func textWidth(s string, scale int) int { return len(s) * 6 * scale }

// drawPaletteName sets the palette to the right of the brightness readout, on
// the same line, so the key's two most-changed facts share one band.
func drawPaletteName(img *image.RGBA, st Status) {
	name := st.Palette
	if name == "" {
		return
	}
	// The percentage occupies the left; the palette gets what remains.
	const left = 52
	avail := Size - left - 5
	scale := 2
	if textWidth(name, scale) > avail {
		scale = 1
	}
	lines := fitLines(name, scale, avail, 2)
	if lines == nil {
		// Too long even wrapped: keep the first word rather than clipping.
		if i := strings.Index(name, " "); i > 0 {
			name = name[:i]
		}
		scale = 1
		lines = []string{name}
	}
	y := 78
	if len(lines) == 2 && scale == 1 {
		y = 75
	}
	for _, ln := range lines {
		x := left + (avail-textWidth(ln, scale))/2
		drawText(img, ln, x+1, y+1, scale, color.RGBA{0, 0, 0, 255}, 0.7)
		drawText(img, ln, x, y, scale, ice, 1.0)
		y += 7*scale + 2
	}
}

// drawFader paints the pool brightness as a bar across the bottom of the key:
// a dim track, a filled portion tinted with the palette, and a bright cap at
// the level so the exact position reads at a glance.
func drawFader(img *image.RGBA, st Status) {
	const (
		x0, x1 = 8, Size - 8
		top, h = 122, 10
	)
	lvl := st.Brightness
	if lvl < 0 {
		lvl = 0
	}
	if lvl > 100 {
		lvl = 100
	}
	w := float64(x1 - x0)
	fill := x0 + int(w*float64(lvl)/100)

	// When every light is off the level is only what the lights will come back
	// at, so the bar is drawn muted rather than reading as "something is lit".
	live := st.LightsOn > 0
	for y := top; y < top+h; y++ {
		for x := x0; x < x1; x++ {
			c := blend(img.RGBAAt(x, y), color.RGBA{28, 16, 46, 255}, 0.92)
			if x < fill {
				col := lerp(neon, magenta, float64(x-x0)/w)
				a := 0.95
				if !live {
					a = 0.38
				}
				c = blend(c, col, a)
			}
			img.Set(x, y, c)
		}
	}
	if lvl > 0 {
		capCol := ice
		if !live {
			capCol = dim
		}
		for y := top - 2; y < top+h+2; y++ {
			for x := fill - 2; x <= fill; x++ {
				if x < x0 || x >= x1 || y < 0 || y >= Size {
					continue
				}
				img.Set(x, y, blend(img.RGBAAt(x, y), capCol, 0.95))
			}
		}
	}
}

// drawPattern previews the colour scheme in the top-left: when the gradient is
// on, stepped bars each carrying a different palette colour, which is what the
// lights will actually be spread across; when it is off, one solid block in the
// palette's lead colour, because every light shares that one colour.
func drawPattern(img *image.RGBA, st Status) {
	const (
		x0   = 6
		y0   = 5
		w    = 34
		barH = 4
		gap  = 2
	)
	pick := func(t float64) color.RGBA {
		if len(st.Swatches) == 0 {
			return lerp(neon, magenta, t)
		}
		i := int(t * float64(len(st.Swatches)-1))
		if i < 0 {
			i = 0
		}
		if i >= len(st.Swatches) {
			i = len(st.Swatches) - 1
		}
		c := st.Swatches[i]
		return color.RGBA{uint8(c.R), uint8(c.G), uint8(c.B), 255}
	}
	if st.Gradient {
		for i := 0; i < 3; i++ {
			fillRect(img, x0, y0+i*(barH+gap), w, barH, pick(float64(i)/2), 0.98)
		}
		return
	}
	fillRect(img, x0, y0, w, 3*barH+2*gap, pick(0.5), 0.98)
}

// fillRect paints a solid run of pixels, clipped to the key.
func fillRect(img *image.RGBA, x, y, w, h int, col color.RGBA, a float64) {
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			if px < 0 || py < 0 || px >= Size || py >= Size {
				continue
			}
			img.Set(px, py, blend(img.RGBAAt(px, py), col, a))
		}
	}
}

// itoa avoids pulling strconv in for one small conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// Nav is one palette direction key: the palette a press will land on, drawn
// with an arrow so the key says where it goes rather than where you are.
type Nav struct {
	Name     string
	Swatches []Swatch
	Forward  bool // true = next (">"), false = previous ("<")
}

// NavKey renders a palette next/previous key as a base64 PNG.
func NavKey(n Nav) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))
	grid(img)
	drawArrow(img, n.Forward)

	kicker := "PREV"
	if n.Forward {
		kicker = "NEXT"
	}
	drawText(img, kicker, (Size-textWidth(kicker, 1))/2, 8, 1, dim, 1.0)
	drawNavName(img, n.Name)

	if len(n.Swatches) > 0 {
		const top, h = 116, 16
		w := float64(Size-16) / float64(len(n.Swatches))
		for i, s := range n.Swatches {
			c := color.RGBA{uint8(s.R), uint8(s.G), uint8(s.B), 255}
			for x := 8 + int(float64(i)*w); x < 8+int(float64(i+1)*w) && x < Size-8; x++ {
				for y := top; y < top+h; y++ {
					img.Set(x, y, c)
				}
			}
		}
		for x := 8; x < Size-8; x++ {
			img.Set(x, top, blend(img.RGBAAt(x, top), color.RGBA{255, 255, 255, 255}, 0.18))
		}
	}
	border(img)
	return encode(img)
}

// drawArrow paints a chevron pointing the way the key cycles: ">" for next,
// "<" for previous. The strokes lie at dx = h-|dy| for ">", mirrored for "<".
func drawArrow(img *image.RGBA, forward bool) {
	const cx, cy, h, thick = 72.0, 34.0, 13.0, 3.4
	for y := int(cy - h - 3); y <= int(cy+h+3); y++ {
		for x := int(cx - h - 3); x <= int(cx+h+3); x++ {
			if x < 0 || y < 0 || x >= Size || y >= Size {
				continue
			}
			dy := float64(y) - cy
			if math.Abs(dy) > h {
				continue
			}
			dx := float64(x) - cx
			if !forward {
				dx = -dx
			}
			d := math.Abs(dx-(h-math.Abs(dy))) / math.Sqrt2
			a := 1.0 - (d-thick/2)/1.4
			if d <= thick/2 {
				a = 1
			}
			if a <= 0 {
				continue
			}
			img.Set(x, y, blend(img.RGBAAt(x, y), lerp(neon, magenta, (dy+h)/(2*h)), a))
		}
	}
}

// drawNavName centres the destination palette name between arrow and swatches.
func drawNavName(img *image.RGBA, name string) {
	if name == "" {
		return
	}
	scale := 2
	lines := fitLines(name, scale, Size-10, 2)
	if lines == nil {
		scale = 1
		lines = fitLines(name, scale, Size-8, 2)
	}
	if lines == nil {
		return
	}
	y := 62
	if len(lines) == 2 {
		y = 54
	}
	for _, ln := range lines {
		x := (Size - textWidth(ln, scale)) / 2
		drawText(img, ln, x+1, y+1, scale, color.RGBA{0, 0, 0, 255}, 0.7)
		drawText(img, ln, x, y, scale, ice, 1.0)
		y += 7*scale + 3
	}
}

// Level renders a read-only brightness key: the level as a big number inside a
// filled arc. It is a readout, not a control — the app's slider owns the value.
func Level(pct int, live bool) (string, error) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))
	grid(img)

	// Gap centred at the bottom: the arc starts 45 degrees past straight-down
	// (lower-left) and sweeps 270 degrees clockwise to lower-right.
	const cx, cy, rad = 72.0, 76.0, 44.0
	const start, sweep = 225.0, 270.0
	frac := float64(pct) / 100
	for y := 0; y < Size; y++ {
		for x := 0; x < Size; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d := math.Hypot(dx, dy)
			if math.Abs(d-rad) > 6 {
				continue
			}
			ang := math.Mod(math.Atan2(dy, dx)*180/math.Pi+450, 360)
			rel := math.Mod(ang-start+360, 360)
			if rel > sweep {
				continue
			}
			a := 1.0 - (math.Abs(d-rad)-3.2)/1.6
			if math.Abs(d-rad) <= 3.2 {
				a = 1
			}
			if a <= 0 {
				continue
			}
			col := color.RGBA{40, 24, 60, 255}
			if rel <= sweep*frac {
				col = lerp(neon, magenta, rel/sweep)
				if !live {
					col = lerp(col, dim, 0.65)
				}
			}
			img.Set(x, y, blend(img.RGBAAt(x, y), col, a))
		}
	}

	num := itoa(pct)
	nx, ny := (Size-textWidth(num, 3))/2, 62
	numCol := ice
	if !live {
		numCol = dim
	}
	drawText(img, num, nx+1, ny+1, 3, color.RGBA{0, 0, 0, 255}, 0.7)
	drawText(img, num, nx, ny, 3, numCol, 1.0)
	drawText(img, "%", (Size-textWidth("%", 1))/2, ny+24, 1, dim, 1.0)
	drawText(img, "BRIGHTNESS", (Size-textWidth("BRIGHTNESS", 1))/2, 122, 1, dim, 1.0)

	border(img)
	return encode(img)
}

// grid paints the shared background: near-black with a faint purple grid.
func grid(img *image.RGBA) {
	for y := 0; y < Size; y++ {
		for x := 0; x < Size; x++ {
			c := blend(bg, color.RGBA{18, 8, 32, 255}, 0.5*(1-float64(y)/Size))
			const cell = 24
			gx := math.Abs(math.Mod(float64(x), cell) - cell/2)
			gy := math.Abs(math.Mod(float64(y), cell) - cell/2)
			if gx > cell/2-1.2 || gy > cell/2-1.2 {
				c = blend(c, neon, 0.10)
			}
			img.Set(x, y, c)
		}
	}
}

// encode turns a finished key into the data URI setImage expects.
func encode(img *image.RGBA) (string, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// drawHeadline is the key's answer to "what is on right now", set in the
// largest type the key can carry. One lit light is named; several are counted.
func drawHeadline(img *image.RGBA, st Status) {
	// A dark plate behind the text so the wave never fights the glyphs.
	for y := 24; y < 72; y++ {
		for x := 4; x < Size-4; x++ {
			// Stronger in the middle of the band, feathered at its edges, so
			// the wave fades out behind the text instead of stopping hard.
			a := 0.80
			if d := y - 24; d < 4 {
				a *= float64(d) / 4
			} else if d := 71 - y; d < 4 {
				a *= float64(d) / 4
			}
			img.Set(x, y, blend(img.RGBAAt(x, y), color.RGBA{4, 2, 10, 255}, a))
		}
	}

	switch {
	case st.Total == 0:
		w := textWidth("NO LIGHTS", 2)
		drawText(img, "NO LIGHTS", (Size-w)/2, 42, 2, dim, 1.0)
	case st.LightsOn == 0:
		w := textWidth("ALL OFF", 3)
		drawText(img, "ALL OFF", (Size-w)/2+1, 41, 3, color.RGBA{0, 0, 0, 255}, 0.7)
		drawText(img, "ALL OFF", (Size-w)/2, 40, 3, dim, 1.0)
	case st.LightsOn == 1 && len(st.OnNames) == 1:
		// Exactly one lit: name it. "1 ON" would waste the key's best space
		// when the useful fact is which light it is.
		drawNameBlock(img, st.OnNames[0])
	default:
		num := itoa(st.LightsOn)
		nx := (Size - textWidth(num, 5)) / 2
		drawText(img, num, nx+2, 30, 5, color.RGBA{0, 0, 0, 255}, 0.75)
		drawText(img, num, nx, 28, 5, ice, 1.0)
		tag := "OF " + itoa(st.Total) + " ON"
		drawText(img, tag, (Size-textWidth(tag, 1))/2, 63, 1, lerp(neon, magenta, 0.5), 1.0)
	}
}

// drawNameBlock centres a single lit light's name across up to three lines,
// picking the largest scale that fits so short names read from across a room.
func drawNameBlock(img *image.RGBA, name string) {
	// The headline plate runs y=26..72, so a block may be at most 44px tall.
	// Try each scale largest-first and take the first that genuinely fits.
	for _, scale := range []int{3, 2, 1} {
		maxLines := 44 / (7*scale + 3)
		if maxLines < 1 {
			maxLines = 1
		}
		lines := fitLines(name, scale, Size-10, maxLines)
		if lines == nil {
			continue
		}
		lh := 7*scale + 3
		y := 49 - (len(lines)*lh)/2
		for _, ln := range lines {
			x := (Size - textWidth(ln, scale)) / 2
			drawText(img, ln, x+1, y+1, scale, color.RGBA{0, 0, 0, 255}, 0.7)
			drawText(img, ln, x, y, scale, ice, 1.0)
			y += lh
		}
		drawText(img, "ON", (Size-textWidth("ON", 1))/2, 63, 1, lerp(neon, magenta, 0.5), 1.0)
		return
	}
	// A single unbreakable word too wide even at 1x: clip rather than overflow.
	n := name
	if len(n) > 22 {
		n = n[:22]
	}
	drawText(img, n, (Size-textWidth(n, 1))/2, 44, 1, ice, 1.0)
	drawText(img, "ON", (Size-textWidth("ON", 1))/2, 63, 1, lerp(neon, magenta, 0.5), 1.0)
}

// fitLines wraps s to at most maxLines lines of at most width px at this scale,
// returning nil when it cannot be made to fit.
func fitLines(s string, scale, width, maxLines int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, w := range words {
		if textWidth(w, scale) > width {
			return nil // a single word too wide to break
		}
		try := w
		if cur != "" {
			try = cur + " " + w
		}
		if textWidth(try, scale) <= width {
			cur = try
			continue
		}
		lines = append(lines, cur)
		cur = w
		if len(lines) > maxLines {
			return nil
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > maxLines {
		return nil
	}
	return lines
}

// drawSubline puts the brightness percentage on the left of the lower band,
// with the palette name beside it, so the two live facts share one line.
func drawSubline(img *image.RGBA, st Status) {
	// Dimmed on purpose: brightness is context for the headline, not the
	// headline itself, and the palette beside it carries the brighter ink.
	pct := itoa(st.Brightness) + "%"
	drawText(img, pct, 6, 79, 2, color.RGBA{0, 0, 0, 255}, 0.6)
	drawText(img, pct, 5, 78, 2, lerp(dim, ice, 0.45), 1.0)
}
