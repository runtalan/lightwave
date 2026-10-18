package render

import "image"

// Vacuum is everything a robot vacuum key shows.
type Vacuum struct {
	Name string
	// Label is one short word for what the vacuum is doing: CLEANING, DOCKED...
	Label string
	// Battery is a percentage, or negative when it is not known.
	Battery int
	// Active is true while it is cleaning or heading home, which lights the key.
	Active bool
	// Note, when set, replaces the readout: "OFFLINE", "NO LOGIN", and so on.
	Note string
	// Press says what a press does ("DOCK", "ROOM 1", "START/PAUSE"), so keys
	// set to different actions do not look alike.
	Press string
}

// VacuumKey draws a robot vacuum key: its name, the battery in large type, what
// it is doing underneath, and a bar along the bottom that is the battery level.
func VacuumKey(v Vacuum) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))
	grid(img)

	live := v.Active && v.Note == ""
	drawKeyName(img, v.Name, live)

	switch {
	case v.Note != "":
		drawCentred(img, v.Note, 2, 62, lerp(dim, magenta, 0.55))
	default:
		num := "--"
		if v.Battery >= 0 {
			num = itoa(clampPct(v.Battery)) + "%"
		}
		col := ice
		if !live {
			col = lerp(dim, ice, 0.65)
		}
		drawCentred(img, num, 3, 46, col)
		drawCentred(img, v.Label, 2, 72, lerp(neon, magenta, 0.5))
	}

	if v.Press != "" {
		// Set like a button label, so it reads as what the key does rather than
		// as another readout.
		fillRect(img, 8, 97, Size-16, 20, neon, 0.28)
		if textWidth(v.Press, 2) <= Size-16 {
			drawCentred(img, v.Press, 2, 100, ice)
		} else {
			// A long room name: smaller, rather than running off the key.
			drawCentred(img, clip(v.Press, (Size-16)/6), 1, 104, ice)
		}
	}

	// The bar is the battery; it glows while the vacuum is working.
	fillRect(img, 10, 122, Size-20, 4, dim, 0.35)
	if v.Note == "" && v.Battery > 0 {
		w := (Size - 20) * clampPct(v.Battery) / 100
		for x := 0; x < w; x++ {
			c := lerp(neon, magenta, float64(x)/float64(Size-20))
			if !live {
				c = lerp(c, dim, 0.55)
			}
			fillRect(img, 10+x, 122, 1, 4, c, 1)
		}
	}

	border(img)
	return encode(img)
}

func clampPct(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// clip cuts s to at most n characters.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
