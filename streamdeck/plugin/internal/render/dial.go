package render

import (
	"image"
	"image/color"
	"math"
)

// Colour modes, matching lw.Mode*. Repeated here so render stays free of the
// wire package.
const (
	ModeWarmness = "warmness"
	ModePalette  = "palette"
	ModeSolid    = "solid"
)

// Warmness range Lightwave accepts, in Kelvin.
const (
	MinKelvin = 2000
	MaxKelvin = 6500
)

// StripW and StripH size the touch-strip pixmap: the whole 200x100 panel of
// layouts/dial.json at 2x. Stream Deck fits a pixmap into its slot as if it
// were square-ish, so a wide thin image loses its ends; drawing the full panel
// and leaving the text area transparent keeps every swatch on screen.
const StripW, StripH = 400, 200

// band is where the colours sit on the panel: full width under the text.
var band = image.Rect(24, 124, StripW-24, 180)

// KelvinRGB approximates a white at this colour temperature, using the same
// curve Lightwave sends to lamps that take RGB rather than Kelvin.
func KelvinRGB(k int) Swatch {
	if k < MinKelvin {
		k = MinKelvin
	}
	if k > MaxKelvin {
		k = MaxKelvin
	}
	t := float64(k) / 100
	var r, g, b float64
	if t <= 66 {
		r = 255
		g = 99.4708025861*math.Log(t) - 161.1195681661
		if t <= 19 {
			b = 0
		} else {
			b = 138.5177312231*math.Log(t-10) - 305.0447927307
		}
	} else {
		r = 329.698727446 * math.Pow(t-60, -0.1332047592)
		g = 288.1221695283 * math.Pow(t-60, -0.0755148492)
		b = 255
	}
	ch := func(v float64) int { return max(0, min(255, int(math.Round(v)))) }
	return Swatch{ch(r), ch(g), ch(b)}
}

// warmthAt maps a position across a strip to a temperature. Cool sits on the
// left and warm on the right, because turning a dial right makes it warmer.
func warmthAt(u float64) Swatch {
	return KelvinRGB(MaxKelvin - int(u*float64(MaxKelvin-MinKelvin)))
}

// kelvinPos is the inverse of warmthAt: where a temperature sits, 0..1.
func kelvinPos(k int) float64 {
	u := float64(MaxKelvin-k) / float64(MaxKelvin-MinKelvin)
	return math.Max(0, math.Min(1, u))
}

func rgba(s Swatch) color.RGBA { return color.RGBA{uint8(s.R), uint8(s.G), uint8(s.B), 255} }

// SwatchStrip renders a palette's colours as a touch-strip pixmap.
func SwatchStrip(sw []Swatch) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, StripW, StripH))
	swatchBar(img, band, sw, 1)
	return encode(img)
}

// SolidStrip renders the one colour every light shares.
func SolidStrip(c Swatch) (string, error) {
	return SwatchStrip([]Swatch{c})
}

// WarmStrip renders the warmness range with a marker at the current setting.
func WarmStrip(kelvin int) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, StripW, StripH))
	warmBar(img, band, 1)
	marker(img, band.Min.X, band.Max.X, kelvinPos(kelvin), band.Min.Y-6, band.Max.Y+6)
	return encode(img)
}

// ModeStrip shows the three modes side by side as small previews of what each
// looks like, with the current one lit and the others dimmed.
func ModeStrip(mode string, sw []Swatch, kelvin int) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, StripW, StripH))
	modePills(img, band, mode, sw, kelvin)
	return encode(img)
}

// WarmKey renders a palette key while Lightwave is in Warmness mode. Like the
// palette keys it shows where a press lands: the temperature it will set.
func WarmKey(kelvin int, warmer bool) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))
	grid(img)
	drawArrow(img, warmer)
	kicker := "COOLER"
	if warmer {
		kicker = "WARMER"
	}
	drawText(img, kicker, (Size-textWidth(kicker, 1))/2, 8, 1, dim, 1.0)
	num := itoa(kelvin) + "K"
	nx := (Size - textWidth(num, 3)) / 2
	drawText(img, num, nx+1, 63, 3, color.RGBA{0, 0, 0, 255}, 0.7)
	drawText(img, num, nx, 62, 3, ice, 1.0)
	drawText(img, "WARMNESS", (Size-textWidth("WARMNESS", 1))/2, 94, 1, dim, 1.0)
	warmBar(img, image.Rect(8, 116, Size-8, 132), 1)
	marker(img, 8, Size-8, kelvinPos(kelvin), 112, 136)
	border(img)
	return encode(img)
}

// ModeKey renders the mode switch: the current mode's name and a preview of
// its colours, the three modes in order beneath with the current one lit, and
// the mode a press moves to.
func ModeKey(mode, next string, sw []Swatch, kelvin int) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))
	grid(img)
	drawText(img, "MODE", (Size-textWidth("MODE", 1))/2, 8, 1, dim, 1.0)

	name := modeWord(mode)
	scale := 3
	if textWidth(name, scale) > Size-10 {
		scale = 2
	}
	nx := (Size - textWidth(name, scale)) / 2
	drawText(img, name, nx+1, 27, scale, color.RGBA{0, 0, 0, 255}, 0.7)
	drawText(img, name, nx, 26, scale, ice, 1.0)

	preview := image.Rect(8, 60, Size-8, 80)
	switch mode {
	case ModeWarmness:
		fillRect(img, preview.Min.X, preview.Min.Y, preview.Dx(), preview.Dy(), rgba(KelvinRGB(kelvin)), 1)
		label := itoa(kelvin) + "K"
		drawText(img, label, (Size-textWidth(label, 1))/2, 67, 1, color.RGBA{0, 0, 0, 255}, 0.8)
	case ModeSolid:
		if c, ok := centre(sw); ok {
			fillRect(img, preview.Min.X, preview.Min.Y, preview.Dx(), preview.Dy(), rgba(c), 1)
		}
	default:
		swatchBar(img, preview, sw, 1)
	}

	modePills(img, image.Rect(8, 92, Size-8, 106), mode, sw, kelvin)

	hint := "NEXT " + modeWord(next)
	drawText(img, hint, (Size-textWidth(hint, 1))/2, 122, 1, lerp(neon, magenta, 0.5), 1.0)
	border(img)
	return encode(img)
}

// modeWord is the short name a key has room for.
func modeWord(mode string) string {
	switch mode {
	case ModeWarmness:
		return "WARM"
	case ModePalette:
		return "PALETTE"
	case ModeSolid:
		return "SOLID"
	}
	return mode
}

func centre(sw []Swatch) (Swatch, bool) {
	if len(sw) == 0 {
		return Swatch{}, false
	}
	return sw[len(sw)/2], true
}

// modePills draws Warmness, Palette and Solid previews across r.
func modePills(img *image.RGBA, r image.Rectangle, mode string, sw []Swatch, kelvin int) {
	const gap = 6
	w := (r.Dx() - 2*gap) / 3
	for i, m := range []string{ModeWarmness, ModePalette, ModeSolid} {
		x0 := r.Min.X + i*(w+gap)
		cell := image.Rect(x0, r.Min.Y, x0+w, r.Max.Y)
		a := 0.28
		if m == mode {
			a = 1
		}
		switch m {
		case ModeWarmness:
			warmBar(img, cell, a)
		case ModePalette:
			swatchBar(img, cell, sw, a)
		case ModeSolid:
			if c, ok := centre(sw); ok {
				fillRect(img, cell.Min.X, cell.Min.Y, cell.Dx(), cell.Dy(), rgba(c), a)
			}
		}
		if m == mode {
			outline(img, cell.Inset(-2), ice)
		}
	}
}

// swatchBar paints colours side by side across r.
func swatchBar(img *image.RGBA, r image.Rectangle, sw []Swatch, a float64) {
	if len(sw) == 0 {
		fillRect(img, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), dim, a)
		return
	}
	w := float64(r.Dx()) / float64(len(sw))
	for i, s := range sw {
		x0 := r.Min.X + int(float64(i)*w)
		x1 := r.Min.X + int(float64(i+1)*w)
		fillRect(img, x0, r.Min.Y, x1-x0, r.Dy(), rgba(s), a)
	}
}

// warmBar paints the cool-to-warm range across r.
func warmBar(img *image.RGBA, r image.Rectangle, a float64) {
	for x := r.Min.X; x < r.Max.X; x++ {
		c := rgba(warmthAt(float64(x-r.Min.X) / float64(max(1, r.Dx()-1))))
		fillRect(img, x, r.Min.Y, 1, r.Dy(), c, a)
	}
}

// marker draws a bright cap at fraction u between x0 and x1.
func marker(img *image.RGBA, x0, x1 int, u float64, y0, y1 int) {
	x := x0 + int(u*float64(x1-x0-1))
	fillRect(img, x-3, y0, 7, y1-y0, color.RGBA{0, 0, 0, 255}, 0.75)
	fillRect(img, x-1, y0+1, 3, y1-y0-2, ice, 1)
}

// outline draws a 2px frame just inside r.
func outline(img *image.RGBA, r image.Rectangle, col color.RGBA) {
	fillRect(img, r.Min.X, r.Min.Y, r.Dx(), 2, col, 1)
	fillRect(img, r.Min.X, r.Max.Y-2, r.Dx(), 2, col, 1)
	fillRect(img, r.Min.X, r.Min.Y, 2, r.Dy(), col, 1)
	fillRect(img, r.Max.X-2, r.Min.Y, 2, r.Dy(), col, 1)
}
