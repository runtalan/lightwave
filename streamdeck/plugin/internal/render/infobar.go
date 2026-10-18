package render

import (
	"image"
	"image/color"
)

// The Stream Deck Neo's info bar is 232x50. Drawn at 2x, like the keys, so it
// stays sharp, and exactly the slot's shape so Stream Deck has nothing to crop.
const InfobarW, InfobarH = 464, 100

// Infobar is everything the vacuum's info bar shows.
type Infobar struct {
	// Label is what the vacuum is doing, one word: CLEANING, DOCKED...
	Label string
	// Room is the room it is in, when that is known. Empty otherwise.
	Room string
	// Battery is a percentage, or negative when not known.
	Battery int
	// Minutes and Area are the current (or last) run, negative when not known.
	Minutes, Area int
	// Active is true while it is cleaning or heading home, which lights the bar.
	Active bool
	// Error is the vacuum's fault code, zero when there is none.
	Error int
	// Note, when set, replaces everything: "OFFLINE", "NO LOGIN", and so on.
	Note string
}

// InfobarImage draws the vacuum's info bar: what it is doing on top, and below
// that the battery and how much of the run is done.
func InfobarImage(v Infobar) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, InfobarW, InfobarH))
	for y := 0; y < InfobarH; y++ {
		for x := 0; x < InfobarW; x++ {
			img.Set(x, y, blend(bg, color.RGBA{18, 8, 32, 255}, 0.55*(1-float64(y)/InfobarH)))
		}
	}

	live := v.Active && v.Note == "" && v.Error == 0
	// A bar down the left edge, lit while it works: the info bar's version of
	// the keys' two states.
	for y := 8; y < InfobarH-8; y++ {
		c := lerp(neon, magenta, float64(y-8)/float64(InfobarH-16))
		if !live {
			c = lerp(c, dim, 0.7)
		}
		fillRect(img, 8, y, 6, 1, c, 1)
	}

	left := 28
	top := infoTop(v)
	switch {
	case v.Note != "":
		drawFit(img, v.Note, left, 30, 3, InfobarW-left-12, lerp(dim, magenta, 0.6))
		border(infoBorder(img))
		return encode(img)
	default:
		col := ice
		if !live {
			col = lerp(dim, ice, 0.65)
		}
		if v.Error != 0 {
			col = magenta
		}
		drawFit(img, top, left, 10, 3, InfobarW-left-12, col)
	}

	// Battery: a bar and the number.
	const barX, barY, barW, barH = 28, 62, 150, 16
	fillRect(img, barX, barY, barW, barH, dim, 0.3)
	if v.Battery >= 0 {
		w := barW * clampPct(v.Battery) / 100
		for x := 0; x < w; x++ {
			c := lerp(neon, magenta, float64(x)/barW)
			if !live {
				c = lerp(c, dim, 0.5)
			}
			fillRect(img, barX+x, barY, 1, barH, c, 1)
		}
	}
	pct := "--"
	if v.Battery >= 0 {
		pct = itoa(clampPct(v.Battery)) + "%"
	}
	numCol := ice
	if !live {
		numCol = lerp(dim, ice, 0.65)
	}
	drawText(img, pct, barX+barW+12, barY+1, 2, numCol, 1)

	// The run so far, to the right of the battery.
	if run := runText(v); run != "" {
		x := barX + barW + 12 + textWidth("100%", 2) + 14
		drawFit(img, run, x, barY+1, 2, InfobarW-x-12, lerp(neon, magenta, 0.5))
	}

	border(infoBorder(img))
	return encode(img)
}

// infoTop is the top line: the room and what it is doing, or the fault.
func infoTop(v Infobar) string {
	if v.Error != 0 {
		return "ERROR " + itoa(v.Error)
	}
	if v.Room != "" {
		return v.Room + " " + v.Label
	}
	return v.Label
}

// runText is "12 MIN 8 M2": how long, and how much floor, the run has covered.
func runText(v Infobar) string {
	out := ""
	if v.Minutes >= 0 {
		out = itoa(v.Minutes) + " MIN"
	}
	if v.Area >= 0 {
		if out != "" {
			out += " "
		}
		out += itoa(v.Area) + " M2"
	}
	return out
}

// drawFit draws s at the largest scale up to max that fits width px, and
// cuts it short rather than run off the bar.
func drawFit(img *image.RGBA, s string, x, y, max, width int, col color.RGBA) {
	scale := max
	for scale > 1 && textWidth(s, scale) > width {
		scale--
	}
	if n := width / (6 * scale); len(s) > n && n > 0 {
		s = s[:n]
	}
	// Centre the smaller type on the line it replaces.
	y += (max - scale) * 7 / 2
	drawText(img, s, x+1, y+1, scale, color.RGBA{0, 0, 0, 255}, 0.7)
	drawText(img, s, x, y, scale, col, 1)
}

// infoBorder draws the bar's outline, the same faint neon as the keys, and
// returns the image.
func infoBorder(img *image.RGBA) *image.RGBA {
	b := img.Bounds()
	for i := b.Min.X; i < b.Max.X; i++ {
		for _, y := range []int{b.Min.Y, b.Max.Y - 1} {
			img.Set(i, y, blend(img.RGBAAt(i, y), neon, 0.5))
		}
	}
	for j := b.Min.Y; j < b.Max.Y; j++ {
		for _, x := range []int{b.Min.X, b.Max.X - 1} {
			img.Set(x, j, blend(img.RGBAAt(x, j), neon, 0.5))
		}
	}
	return img
}
