package render

import (
	"image"
	"image/color"
	"strconv"
)

// Plug is everything a smart plug key shows.
type Plug struct {
	Name string
	On   bool
	// Watts is the plug's current draw. Nil when it has no meter, or no reading
	// yet; an on key then just says ON.
	Watts *float64
	// Note, when set, replaces the readout: "OFFLINE", "NO LOGIN", and so on.
	// It is for when the key cannot say what the plug is doing.
	Note string
}

// PlugKey draws a smart plug key: the plug's name, then either the live power
// draw in large type, or OFF, or a note about why neither is known. Like the
// other readouts it is drawn rather than switched between fixed images, because
// the number changes every few seconds.
func PlugKey(p Plug) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))
	grid(img)

	live := p.On && p.Note == ""
	drawPlugName(img, p.Name, live)

	switch {
	case p.Note != "":
		drawCentred(img, p.Note, 2, 62, lerp(dim, magenta, 0.55))
	case !p.On:
		drawCentred(img, "OFF", 4, 56, dim)
	case p.Watts == nil:
		drawCentred(img, "ON", 4, 56, ice)
	default:
		num := FormatWatts(*p.Watts)
		scale := 4
		if textWidth(num, scale) > Size-16 {
			scale = 3
		}
		drawCentred(img, num, scale, 52, ice)
		drawCentred(img, "WATTS", 1, 100, lerp(neon, magenta, 0.5))
	}

	// A bar along the bottom, lit while the plug is: the same cue the other
	// keys give with their two states, kept when the image replaces the state.
	if live {
		for x := 10; x < Size-10; x++ {
			fillRect(img, x, 122, 1, 4, lerp(neon, magenta, float64(x-10)/float64(Size-20)), 1)
		}
	} else {
		fillRect(img, 10, 122, Size-20, 4, dim, 0.35)
	}

	border(img)
	return encode(img)
}

// FormatWatts is how a draw reads on a key: a decimal while it is small enough
// for one to matter, whole watts above that.
func FormatWatts(w float64) string {
	if w < 0 {
		w = 0
	}
	if w < 10 {
		return strconv.FormatFloat(w, 'f', 1, 64)
	}
	return strconv.FormatFloat(w, 'f', 0, 64)
}

// drawCentred sets s horizontally centred with its top at y, shadowed so it
// holds against the grid.
func drawCentred(img *image.RGBA, s string, scale, y int, col color.RGBA) {
	// textWidth counts the gap after the last glyph, which would pull the
	// text a pixel-column left of centre at larger scales.
	x := (Size - (textWidth(s, scale) - scale)) / 2
	drawText(img, s, x+1, y+1, scale, color.RGBA{0, 0, 0, 255}, 0.7)
	drawText(img, s, x, y, scale, col, 1.0)
}

// drawPlugName sets the plug's name across the top: two lines of 2x type when
// it fits, otherwise two lines of the small type, cut short rather than
// overflowing the key.
func drawPlugName(img *image.RGBA, name string, live bool) {
	col := ice
	if !live {
		col = lerp(dim, ice, 0.4)
	}
	if lines := fitLines(name, 2, Size-16, 2); lines != nil {
		for i, l := range lines {
			drawCentred(img, l, 2, 10+i*18, col)
		}
		return
	}
	lines := fitLines(name, 1, Size-16, 2)
	if lines == nil {
		// One word too long to break, or too many words: take what fits.
		max := (Size - 16) / 6
		if len(name) > max {
			name = name[:max]
		}
		lines = []string{name}
	}
	for i, l := range lines {
		drawCentred(img, l, 1, 12+i*12, col)
	}
}
