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
)

// Size is the key resolution. 144 is the @2x size Stream Deck expects; it
// downscales cleanly to 72 on non-retina hardware.
const Size = 144

// Swatch is one palette colour.
type Swatch struct{ R, G, B int }

// Status is everything the indicator draws.
type Status struct {
	Palette  string   // palette name, e.g. "Ocean"
	Swatches []Swatch // the palette's colours, left to right
	Dancing  bool     // colour fade running
	LightsOn int      // how many lights are lit
	Total    int      // how many lights are bound
	Phase    float64  // 0..1, advances over time to animate the wave
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
	drawSwatches(img, st)
	drawPaletteName(img, st)
	drawFadeDot(img, st)
	drawLightCount(img, st)
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
	const midY = 62.0
	for x := 0; x < Size; x++ {
		u := float64(x) / Size
		for _, w := range []struct {
			amp, freq, off, weight, alpha float64
		}{
			{11, 2.1, 0.0, 2.4, 0.95},
			{7, 3.0, 1.7, 1.6, 0.55},
			{15, 1.4, 3.1, 1.2, 0.32},
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
	const top, h = 96, 18
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
	cx, cy := 124.0, 20.0
	r := 6.0
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

// drawLightCount shows lit/total as a row of pips along the top-left.
func drawLightCount(img *image.RGBA, st Status) {
	if st.Total == 0 {
		return
	}
	n := st.Total
	if n > 9 {
		n = 9
	}
	for i := 0; i < n; i++ {
		x := 10 + i*11
		y := 20
		lit := i < st.LightsOn
		col := dim
		a := 0.75
		if lit {
			col = lerp(neon, magenta, float64(i)/float64(max(n-1, 1)))
			a = 1.0
		}
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				d := math.Hypot(float64(dx), float64(dy))
				alpha := 1.0 - (d-2.2)/1.2
				if d <= 2.2 {
					alpha = 1
				}
				if alpha <= 0 {
					continue
				}
				px, py := x+dx, y+dy
				if px < 0 || py < 0 || px >= Size || py >= Size {
					continue
				}
				img.Set(px, py, blend(img.RGBAAt(px, py), col, alpha*a))
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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

// drawPaletteName centres the palette label above the swatch strip, shrinking
// the scale for long names ("DEEP ORANGES") so they still fit the key.
func drawPaletteName(img *image.RGBA, st Status) {
	name := st.Palette
	if name == "" {
		return
	}
	scale := 2
	if textWidth(name, scale) > Size-12 {
		scale = 1
	}
	// Still too wide at 1x: drop the second word rather than clipping mid-glyph.
	if textWidth(name, scale) > Size-8 {
		for i, r := range name {
			if r == ' ' && i > 0 {
				name = name[:i]
				break
			}
		}
	}
	w := textWidth(name, scale)
	x := (Size - w) / 2
	y := 78
	// Shadow first so the label stays readable over the wave.
	drawText(img, name, x+1, y+1, scale, color.RGBA{0, 0, 0, 255}, 0.65)
	drawText(img, name, x, y, scale, ice, 1.0)
}
