package render

import (
	"fmt"
	"image"
	"sync"
)

// Every picture here is a pure function of its arguments, and Lightwave pushes
// its whole state on every change, so most keys are asked for the same picture
// again and again: a brightness sweep changes one gauge, yet every key on the
// deck is redrawn. Drawing and PNG-encoding a key costs about a millisecond, so
// finished pictures are kept, keyed by what they were drawn from.
//
// The exported functions below are the cached front of the drawing functions
// of the same name in lower case.

// memoLimit bounds the cache. The status key's wave makes a new picture every
// frame, so old entries have to go; dropping them all at once is simple and,
// at a few KB each, cheap to refill.
const memoLimit = 512

var memo struct {
	sync.Mutex
	m map[string]string
}

func cached(key string, draw func() (string, error)) (string, error) {
	memo.Lock()
	img, ok := memo.m[key]
	memo.Unlock()
	if ok {
		return img, nil
	}
	img, err := draw()
	if err != nil {
		return "", err
	}
	memo.Lock()
	if memo.m == nil || len(memo.m) >= memoLimit {
		memo.m = make(map[string]string, memoLimit)
	}
	memo.m[key] = img
	memo.Unlock()
	return img, nil
}

func memoKey(name string, args ...any) string {
	return fmt.Sprintf("%s%#v", name, args)
}

// Indicator renders the status key as a base64 PNG ready for setImage.
func Indicator(st Status) (string, error) {
	return cached(memoKey("Indicator", st), func() (string, error) { return indicator(st) })
}

// NavKey renders a previous/next palette key.
func NavKey(n Nav) (string, error) {
	return cached(memoKey("NavKey", n), func() (string, error) { return navKey(n) })
}

// PaletteKey renders a key that jumps to one named palette.
func PaletteKey(name string, swatches []Swatch) (string, error) {
	return cached(memoKey("PaletteKey", name, swatches), func() (string, error) { return paletteKey(name, swatches) })
}

// Level renders the 0-100 arc gauge.
func Level(pct int, live bool) (string, error) {
	return cached(memoKey("Level", pct, live), func() (string, error) { return level(pct, live) })
}

// SwatchStrip renders a palette's colours across a dial's touch strip.
func SwatchStrip(sw []Swatch) (string, error) {
	return cached(memoKey("SwatchStrip", sw), func() (string, error) { return swatchStrip(sw) })
}

// SolidStrip renders one colour across a dial's touch strip.
func SolidStrip(c Swatch) (string, error) {
	return cached(memoKey("SolidStrip", c), func() (string, error) { return solidStrip(c) })
}

// WarmStrip renders the room's white temperature on a touch strip.
func WarmStrip(kelvin int) (string, error) {
	return cached(memoKey("WarmStrip", kelvin), func() (string, error) { return warmStrip(kelvin) })
}

// FrontStrip renders the front light's temperature on a touch strip.
func FrontStrip(kelvin int) (string, error) {
	return cached(memoKey("FrontStrip", kelvin), func() (string, error) { return frontStrip(kelvin) })
}

// LevelStrip renders a 0-100 level as a bar filled from the left.
func LevelStrip(pct int) (string, error) {
	return cached(memoKey("LevelStrip", pct), func() (string, error) { return levelStrip(pct) })
}

// PositionStrip renders place i of n on a touch strip.
func PositionStrip(i, n int) (string, error) {
	return cached(memoKey("PositionStrip", i, n), func() (string, error) { return positionStrip(i, n) })
}

// SceneStrip renders a scene's place in the list beside its icon. The icon is
// keyed by identity: the plugin loads each one once and keeps it.
func SceneStrip(icon image.Image, i, n int) (string, error) {
	key := fmt.Sprintf("SceneStrip%p/%d/%d", icon, i, n)
	return cached(key, func() (string, error) { return sceneStrip(icon, i, n) })
}

// ModeStrip renders the colour mode on a touch strip.
func ModeStrip(mode string, sw []Swatch, kelvin int) (string, error) {
	return cached(memoKey("ModeStrip", mode, sw, kelvin), func() (string, error) { return modeStrip(mode, sw, kelvin) })
}

// WarmKey renders a warmer/cooler key.
func WarmKey(kelvin int, warmer bool) (string, error) {
	return cached(memoKey("WarmKey", kelvin, warmer), func() (string, error) { return warmKey(kelvin, warmer) })
}

// ModeKey renders the colour mode key.
func ModeKey(mode, next string, sw []Swatch, kelvin int) (string, error) {
	return cached(memoKey("ModeKey", mode, next, sw, kelvin), func() (string, error) { return modeKey(mode, next, sw, kelvin) })
}

// PlugKey renders a smart plug key. Watts is a pointer, so it is keyed by value.
func PlugKey(p Plug) (string, error) {
	watts := "-"
	if p.Watts != nil {
		watts = fmt.Sprint(*p.Watts)
	}
	key := memoKey("PlugKey", p.Name, p.On, watts, p.Note)
	return cached(key, func() (string, error) { return plugKey(p) })
}

// InfobarImage renders the Stream Deck Neo's info bar.
func InfobarImage(v Infobar) (string, error) {
	return cached(memoKey("InfobarImage", v), func() (string, error) { return infobarImage(v) })
}

// VacuumKey renders a vacuum key.
func VacuumKey(v Vacuum) (string, error) {
	return cached(memoKey("VacuumKey", v), func() (string, error) { return vacuumKey(v) })
}
