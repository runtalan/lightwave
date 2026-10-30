package main

import (
	"log"
	"strconv"
	"strings"

	"lightwave-sd/internal/lw"
	"lightwave-sd/internal/render"
	"lightwave-sd/internal/sd"
)

// The Light Brightness and Light Color dials work on one light at a time, the
// same one for both: pushing either moves them to the next light that is on, so
// a pair of dials side by side reads as one light's brightness and colour.

// lightLevelStep is how many percent one detent of the light brightness dial
// moves a light, the same gentle ramp as the main brightness dial.
const lightLevelStep = 2

// lightKelvinStep is how far one detent moves a light's white, as on the
// palette dial in Warmness mode.
const lightKelvinStep = 100

func isLightDial(action string) bool {
	return action == actLightLevel || action == actLightColor
}

// litPads returns the lights that are on, in pad order: the ones the dials
// step through. A light that is off has nothing to show for a turn.
func litPads(st lw.State) []*lw.Pad {
	var out []*lw.Pad
	for n := 1; n <= 9; n++ {
		if p := st.Pad(n); p != nil && p.Bound && p.On {
			out = append(out, p)
		}
	}
	return out
}

// litAfter is the first lit light after pad cur in pad order, wrapping from
// the last back to the first, or nil when none is lit.
func litAfter(st lw.State, cur int) *lw.Pad {
	pads := litPads(st)
	for _, p := range pads {
		if p.Number > cur {
			return p
		}
	}
	if len(pads) > 0 {
		return pads[0]
	}
	return nil
}

// selectedLight is the light the dials are on: cur while it is lit. Once it
// goes off, the dials move on to the next lit light, as a push would, so they
// never sit on a light that is dark. nil when nothing is lit.
func selectedLight(st lw.State, cur int) *lw.Pad {
	if p := st.Pad(cur); p != nil && p.Bound && p.On {
		return p
	}
	return litAfter(st, cur)
}

// nextLight is the pad a push moves to: the next lit light in pad order,
// wrapping from the last back to the first. 0 when nothing is lit.
func nextLight(st lw.State, cur int) int {
	at := selectedLight(st, cur)
	if at == nil {
		return 0
	}
	return litAfter(st, at.Number).Number
}

// lightLevel is how bright a light is, 1-100. Lightwave sends it; older builds
// that do not are worked out from the slider and the light's trim.
func lightLevel(st lw.State, p *lw.Pad) int {
	if p.Level > 0 {
		return p.Level
	}
	trim := p.Trim
	if trim < 1 {
		trim = 100
	}
	return clampInt(st.Brightness*trim/100, 1, 100)
}

// lightLevelCmd is the command one turn of the brightness dial sends for pad:
// clockwise is brighter. ok is false when there is no light to turn; an empty
// cmd means nothing to do.
//
// LEVEL sets the light's share of the main slider so it lands on this level
// now, so the light can sit above or below the rest of the room and the slider
// still carries it with them.
func lightLevelCmd(st lw.State, pad *lw.Pad, ticks int) (cmd string, ok bool) {
	if pad == nil {
		return "", false
	}
	if ticks == 0 {
		return "", true
	}
	level := clampInt(lightLevel(st, pad)+ticks*lightLevelStep, 1, 100)
	return "LEVEL " + strconv.Itoa(pad.Number) + " " + strconv.Itoa(level), true
}

// paletteIndex finds a palette by name in st.Palettes, or -1.
func paletteIndex(st lw.State, name string) int {
	for i, n := range st.Palettes {
		if strings.EqualFold(n, name) {
			return i
		}
	}
	return -1
}

// lightColorCmd is the command one turn of the colour dial sends for pad.
// A light with its own palette steps through the palettes and one with its own
// white goes warmer clockwise, as the room's palette dial does. A light that
// follows the room takes its own colour from where the room is, one step on, so
// the first turn already changes it.
func lightColorCmd(st lw.State, pad *lw.Pad, ticks int) (cmd string, ok bool) {
	if pad == nil {
		return "", false
	}
	if ticks == 0 {
		return "", true
	}
	prefix := "LIGHT_COLOR " + strconv.Itoa(pad.Number) + " "
	warm := func(k int) string {
		return prefix + "WARM " + strconv.Itoa(clampInt(k-ticks*lightKelvinStep, render.MinKelvin, render.MaxKelvin))
	}
	palette := func(from string) (string, bool) {
		n := len(st.Palettes)
		if n == 0 {
			return "", false
		}
		i := paletteIndex(st, from)
		if i < 0 {
			i = max(0, st.PaletteIndex())
		}
		return prefix + "PALETTE " + strconv.Itoa(((i+ticks)%n+n)%n), true
	}
	switch pad.Color {
	case lw.LampWarm:
		return warm(pad.Kelvin), true
	case lw.LampPalette:
		return palette(pad.Palette)
	}
	if st.WarmMode {
		return warm(st.Warmness), true
	}
	return palette(st.Palette)
}

// lightColorTapCmd moves a light on from the room's colour, to a palette of its
// own, to a white of its own, and back to the room. Each starts from what the
// room is showing, so a tap changes what the light is doing without a jump.
func lightColorTapCmd(st lw.State, pad *lw.Pad) string {
	prefix := "LIGHT_COLOR " + strconv.Itoa(pad.Number) + " "
	switch pad.Color {
	case lw.LampPalette:
		return prefix + "WARM " + strconv.Itoa(clampInt(st.Warmness, render.MinKelvin, render.MaxKelvin))
	case lw.LampWarm:
		return prefix + "ROOM"
	}
	i := max(0, st.PaletteIndex())
	return prefix + "PALETTE " + strconv.Itoa(i)
}

// lightDialState returns the state and the light the dials are on, fetching
// the state when no push has arrived yet.
func (p *plugin) lightDialState(context string) (lw.State, *lw.Pad, bool) {
	p.mu.Lock()
	st, have, cur := p.state, p.haveState, p.light
	p.mu.Unlock()
	if !have {
		fresh, err := p.client.Command("STATE")
		if err != nil {
			log.Printf("light dial: %v", err)
			p.sd.ShowAlert(context)
			return st, nil, false
		}
		p.applyState(fresh)
		st = fresh
	}
	return st, selectedLight(st, cur), true
}

// nextLightDial moves both light dials to the next light, remembers it in each
// dial's settings, and redraws them.
func (p *plugin) nextLightDial(st lw.State, have bool) {
	if !have {
		fresh, err := p.client.Command("STATE")
		if err != nil {
			log.Printf("light dial: %v", err)
			return
		}
		p.applyState(fresh)
		st = fresh
	}
	p.mu.Lock()
	p.light = nextLight(st, p.light)
	saved := settings{Light: p.light}
	var dials []*instance
	for _, inst := range p.contexts {
		if isLightDial(inst.action) {
			dials = append(dials, inst)
		}
	}
	p.mu.Unlock()
	for _, inst := range dials {
		p.sd.SetSettings(inst.context, saved)
		p.render(inst, st)
	}
}

// rotateLightLevel turns the brightness of the light the dials are on.
func (p *plugin) rotateLightLevel(ev sd.Event) {
	st, pad, ok := p.lightDialState(ev.Context)
	if !ok {
		return
	}
	cmd, ok := lightLevelCmd(st, pad, ev.Payload.Ticks)
	if !ok {
		p.sd.ShowAlert(ev.Context)
		return
	}
	if cmd != "" {
		p.run(ev.Context, cmd)
	}
}

// rotateLightColor turns the colour of the light the dials are on.
func (p *plugin) rotateLightColor(ev sd.Event) {
	st, pad, ok := p.lightDialState(ev.Context)
	if !ok {
		return
	}
	cmd, ok := lightColorCmd(st, pad, ev.Payload.Ticks)
	if !ok {
		p.sd.ShowAlert(ev.Context)
		return
	}
	if cmd != "" {
		p.run(ev.Context, cmd)
	}
}

// tapLightColor switches the light between the room's colour, its own palette
// and its own white.
func (p *plugin) tapLightColor(ev sd.Event) {
	st, pad, ok := p.lightDialState(ev.Context)
	if !ok {
		return
	}
	if pad == nil {
		p.sd.ShowAlert(ev.Context)
		return
	}
	p.run(ev.Context, lightColorTapCmd(st, pad))
}

// lightName is how a light is named on the touch strip.
func lightName(p *lw.Pad) string {
	if name := clip(strings.TrimSpace(p.Name), 13); name != "" {
		return name
	}
	return "Pad " + strconv.Itoa(p.Number)
}

// renderLightLevel fills the brightness dial's touch strip with the light it
// is on, how bright that light is, and a bar to match.
func (p *plugin) renderLightLevel(inst *instance, st lw.State) {
	p.mu.Lock()
	cur := p.light
	p.mu.Unlock()
	pad := selectedLight(st, cur)
	if pad == nil {
		p.sd.SetFeedback(inst.context, map[string]any{"title": "Light Brightness", "value": "No lights on", "strip": ""})
		return
	}
	p.renderPadLevel(inst, st, pad)
}

// renderPadLevel fills a brightness touch strip with one light: its name, its
// place among the lights that are on, how bright it is, and a bar to match.
func (p *plugin) renderPadLevel(inst *instance, st lw.State, pad *lw.Pad) {
	pads := litPads(st)
	place := 1
	for i, q := range pads {
		if q.Number == pad.Number {
			place = i + 1
		}
	}
	title := lightName(pad) + " · " + strconv.Itoa(place) + "/" + strconv.Itoa(len(pads))
	level := lightLevel(st, pad)
	fb := map[string]any{
		"title": title,
		"value": strconv.Itoa(level) + "%",
	}
	if strip, err := render.LevelStrip(level); err == nil {
		fb["strip"] = strip
	} else {
		log.Printf("light strip render: %v", err)
	}
	p.sd.SetFeedback(inst.context, fb)
}

// allLevels is the range of brightness across the lights that are on, or the
// slider's level when none is.
func allLevels(st lw.State) (lo, hi int) {
	pads := litPads(st)
	if len(pads) == 0 {
		return st.Brightness, st.Brightness
	}
	lo, hi = 100, 0
	for _, pad := range pads {
		level := lightLevel(st, pad)
		lo, hi = min(lo, level), max(hi, level)
	}
	return lo, hi
}

// brightnessPick is the light the main brightness dial is on: pick while that
// light is lit, otherwise nil, which is every light at once.
func brightnessPick(st lw.State, pick int) *lw.Pad {
	if p := st.Pad(pick); p != nil && p.Bound && p.On {
		return p
	}
	return nil
}

// nextBrightnessPick is where a tap moves the main brightness dial: from All
// to the first light that is on, through each lit light in pad order, and
// from the last back to All (0).
func nextBrightnessPick(st lw.State, pick int) int {
	cur := 0
	if brightnessPick(st, pick) != nil {
		cur = pick
	}
	for _, p := range litPads(st) {
		if p.Number > cur {
			return p.Number
		}
	}
	return 0
}

// renderBrightnessDial fills the main brightness dial's touch strip with the
// light it is on, or every light when it is on All.
func (p *plugin) renderBrightnessDial(inst *instance, st lw.State) {
	p.mu.Lock()
	pick := inst.pick
	p.mu.Unlock()
	if pad := brightnessPick(st, pick); pad != nil {
		p.renderPadLevel(inst, st, pad)
		return
	}
	p.renderAllLevels(inst, st)
}

// renderAllLevels fills the main brightness dial's touch strip: how many lights
// are on, how bright they are (a range when they differ), and a bar to match
// the brightest.
func (p *plugin) renderAllLevels(inst *instance, st lw.State) {
	on, _ := st.CountOn()
	title := "All Lights · " + strconv.Itoa(on) + " on"
	if on == 0 {
		title = "All Lights · off"
	}
	lo, hi := allLevels(st)
	value := strconv.Itoa(hi) + "%"
	if lo != hi {
		value = strconv.Itoa(lo) + "–" + strconv.Itoa(hi) + "%"
	}
	fb := map[string]any{"title": title, "value": value}
	if strip, err := render.LevelStrip(hi); err == nil {
		fb["strip"] = strip
	} else {
		log.Printf("brightness strip render: %v", err)
	}
	p.sd.SetFeedback(inst.context, fb)
}

// renderLightColor fills the colour dial's touch strip with the light it is
// on and what colour it shows: its palette's swatches, its white, or the room's
// colours while it follows the room.
func (p *plugin) renderLightColor(inst *instance, st lw.State) {
	p.mu.Lock()
	cur := p.light
	p.mu.Unlock()
	pad := selectedLight(st, cur)
	if pad == nil {
		p.sd.SetFeedback(inst.context, map[string]any{"title": "Light Color", "value": "No lights on", "strip": ""})
		return
	}
	// A lamp that shows one colour takes the palette's centre swatch, so
	// outline it; a strip shows the whole palette.
	lead := func(sw []render.Swatch) int {
		if pad.Segments {
			return -1
		}
		return len(sw) / 2
	}
	var word, value, strip string
	var err error
	switch {
	case pad.Color == lw.LampWarm:
		word = "Warm"
		value = strconv.Itoa(pad.Kelvin) + "K · " + strconv.Itoa(warmthPct(pad.Kelvin)) + "% warm"
		strip, err = render.WarmStrip(pad.Kelvin)
	case pad.Color == lw.LampPalette:
		word = "Color"
		value = pad.Palette
		sw := swatches(st.PaletteSwatches[pad.Palette])
		strip, err = render.PaletteStrip(sw, lead(sw))
	case st.WarmMode:
		word = "Room"
		value = strconv.Itoa(st.Warmness) + "K · " + strconv.Itoa(warmthPct(st.Warmness)) + "% warm"
		strip, err = render.WarmStrip(st.Warmness)
	default:
		word = "Room"
		value = st.Palette
		sw := swatches(st.Swatches)
		at := -1
		if !st.Gradient {
			// In Solid every light shows the centre swatch.
			at = lead(sw)
		}
		strip, err = render.PaletteStrip(sw, at)
	}
	title := lightName(pad) + " · " + word
	fb := map[string]any{"title": title, "value": value}
	if err != nil {
		log.Printf("light color strip render: %v", err)
	} else {
		fb["strip"] = strip
	}
	p.sd.SetFeedback(inst.context, fb)
}

// clip shortens s to at most n characters, ending in an ellipsis when it cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
