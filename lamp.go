package main

import (
	"fmt"
	"log"
	"time"

	"lightwave/internal/color"
	"lightwave/internal/config"
	"lightwave/internal/govee"
)

// One lamp's own brightness and colour, set apart from the room's.
//
// Brightness stays a share of the slider (the pad's trim), so the slider still
// carries every lamp together; SetSlotLevel just picks the share that puts the
// lamp at a level right now. Colour is either the room's (the zero value) or
// the lamp's own palette or white, which the room's paints and the fade then
// leave alone.

// slotLevelLocked is how bright pad n is at the current slider, 1-100.
// Caller holds a.mu.
func (a *App) slotLevelLocked(n int) int {
	return ignitedBrightness(ignitedBrightness(a.brightness) * a.slots[n-1].EffectiveTrim() / 100)
}

// trimForLevel is the trim that puts a lamp at level with the slider at
// slider: the smallest share that reaches it, so it lands exactly.
func trimForLevel(level, slider int) int {
	wire := ignitedBrightness(slider)
	level = ignitedBrightness(level)
	trim := (level*100 + wire - 1) / wire
	return max(1, min(config.MaxTrim, trim))
}

// SetSlotLevel sets how bright one pad's lamp is, 1-100, by setting its trim
// against the slider as it stands.
func (a *App) SetSlotLevel(slot, level int) error {
	if slot < 1 || slot > 9 {
		return fmt.Errorf("slot must be 1-9")
	}
	a.mu.Lock()
	slider := a.brightness
	a.mu.Unlock()
	_, err := a.SetSlotTrim(slot, trimForLevel(level, slider))
	return err
}

// NudgeBrightness moves every lamp delta points from its own level, clamped to
// 1-100, so lamps set above or below the room keep their distance from it as
// the whole room brightens or dims. The slider moves by the same amount and
// each trim is re-picked to land its lamp on the new level.
func (a *App) NudgeBrightness(delta int) {
	if delta == 0 {
		return
	}
	a.mu.Lock()
	slider := clampBrightness(a.brightness + delta)
	changed := false
	for i := range a.slots {
		if a.slots[i].DeviceID == "" {
			continue
		}
		level := max(1, min(100, a.slotLevelLocked(i+1)+delta))
		trim := trimForLevel(level, slider)
		if trim == 100 {
			// Level with the slider: no trim at all.
			trim = 0
		}
		if trim == a.slots[i].Trim {
			continue
		}
		a.slots[i].Trim = trim
		changed = true
		if ip, _, _ := a.slotLinkLocked(i + 1); ip != "" {
			if a.trimDirty == nil {
				a.trimDirty = map[string]bool{}
			}
			a.trimDirty[ip] = true
		}
	}
	a.brightness = slider
	a.lastBrightTouch = time.Now()
	var snapshot config.SlotFile
	if changed {
		snapshot = config.SlotFile{Configured: a.configured, Slots: append([]config.SlotBinding(nil), a.slots...)}
	}
	a.mu.Unlock()
	if changed {
		if err := config.SaveSlotFile(snapshot); err != nil {
			log.Printf("brightness nudge: save failed: %v", err)
		}
	}
	a.pendingBright.Store(int32(slider))
	select {
	case a.brightKick <- struct{}{}:
	default:
	}
	a.scheduleStateEmit()
}

// releaseOwnColors returns every lamp with a colour of its own, and a bar's
// Govee scene, to the room's, and reports whether any were. Changing the room's palette, warmness or mode
// means the whole room, so a lamp set apart with a dial must not sit it out.
// The caller repaints.
func (a *App) releaseOwnColors() bool {
	a.mu.Lock()
	released := false
	for i := range a.slots {
		if a.slots[i].OwnColor() {
			a.slots[i].Color, a.slots[i].Palette, a.slots[i].Kelvin = config.LampRoom, "", 0
			released = true
		}
	}
	var snapshot config.SlotFile
	if released {
		snapshot = config.SlotFile{Configured: a.configured, Slots: append([]config.SlotBinding(nil), a.slots...)}
	}
	// A Govee scene on a monitor bar's back light is the same thing: the room
	// painters leave it alone while it plays. The bar being switched off is
	// a choice about the light, not a colour, so that one stays.
	sceneOver := a.barScene != "" && a.barScene != barBackOff
	var s config.Settings
	if sceneOver {
		a.barScene = ""
		a.barSceneGen++ // a scene still waiting to be sent must not land
		s = a.settings
		s.BarScene = ""
		a.settings = s
	}
	a.mu.Unlock()
	if released {
		if err := config.SaveSlotFile(snapshot); err != nil {
			log.Printf("lamp colour: save failed: %v", err)
		}
	}
	if sceneOver {
		if err := config.SaveSettings(s); err != nil {
			log.Printf("persist bar scene: %v", err)
		}
	}
	return released || sceneOver
}

// SetSlotColor gives one pad's lamp a colour of its own: mode is
// config.LampPalette with a palette name, config.LampWarm with a temperature in
// Kelvin, or config.LampRoom to follow the room again.
func (a *App) SetSlotColor(slot int, mode, palette string, kelvin int) error {
	if slot < 1 || slot > 9 {
		return fmt.Errorf("slot must be 1-9")
	}
	switch mode {
	case config.LampRoom:
		palette, kelvin = "", 0
	case config.LampPalette:
		i, ok := color.IndexOf(palette)
		if !ok {
			return fmt.Errorf("unknown palette %q", palette)
		}
		palette, kelvin = color.Palettes[i].Name, 0
	case config.LampWarm:
		palette, kelvin = "", clampWarmness(kelvin)
	default:
		return fmt.Errorf("colour must be room, palette or warm")
	}
	a.recordUserActivity()
	a.mu.Lock()
	if slot > len(a.slots) || a.slots[slot-1].DeviceID == "" {
		a.mu.Unlock()
		return fmt.Errorf("pad %d has no light bound", slot)
	}
	b := &a.slots[slot-1]
	wasOwn := b.OwnColor()
	b.Color, b.Palette, b.Kelvin = mode, palette, kelvin
	lit := a.pool[slot]
	ip, _, _ := a.slotLinkLocked(slot)
	snapshot := config.SlotFile{Configured: a.configured, Slots: append([]config.SlotBinding(nil), a.slots...)}
	a.mu.Unlock()
	if err := config.SaveSlotFile(snapshot); err != nil {
		log.Printf("lamp colour: save failed: %v", err)
	}
	switch {
	case !lit || ip == "":
		// Takes effect when the lamp next comes on.
	case wasOwn && mode != config.LampRoom:
		// Still its own colour: only this lamp changes, so a turn of a dial
		// does not repaint the room.
		a.paintSceneOnly(false, func(d lampDest) bool { return d.IP == ip })
	default:
		// Joining or leaving the room changes how the room's palette spreads
		// over the rest, so they all repaint.
		a.applyPaletteToPool()
	}
	a.emitState()
	return nil
}

// paintOwnLamps paints every pooled lamp that has its own colour.
func (a *App) paintOwnLamps(turnOn bool) {
	a.mu.Lock()
	dests := a.poolDestsLocked()
	a.mu.Unlock()
	for _, d := range dests {
		if d.Own != nil {
			a.paintOwn(d, turnOn)
		}
	}
}

// paintOwn paints a lamp with its own colour: a white at its temperature, or
// its palette — the full ramp on a strip that takes zones, the palette's centre
// swatch on a lamp that shows one colour, as Solid does for the room.
func (a *App) paintOwn(d lampDest, turnOn bool) {
	if d.Own == nil {
		return
	}
	a.mu.Lock()
	level := ignitedBrightness(ignitedBrightness(a.brightness) * d.Trim / 100)
	_, hold := a.barSceneLocked()
	a.mu.Unlock()
	if turnOn {
		_ = govee.SendTurn(d.IP, true)
	}
	// A lamp playing a Govee scene keeps it; applyBarScene owns its colour.
	if hold && govee.HasFrontLight(d.Model) {
		return
	}
	var sent color.RGBK
	switch d.Own.Color {
	case config.LampWarm:
		sent = warmRGB(d.Own.Kelvin)
		_ = govee.SendColor(d.IP, sent.R, sent.G, sent.B, sent.Kelvin)
	default:
		i, ok := color.IndexOf(d.Own.Palette)
		if !ok {
			return
		}
		pal := color.Palettes[i]
		if len(pal.Colors) == 0 {
			return
		}
		sent = pal.Colors[len(pal.Colors)/2]
		if govee.SupportsSegments(d.Model) {
			ramp := pal.Gradient(0, govee.GradientBands)
			_ = govee.SendGradient(d.IP, ramp)
			if len(ramp) > 0 {
				sent = ramp[len(ramp)/2]
			}
		} else {
			// A front light's white belongs to its own temperature, as in
			// paintScene, so these lamps take the palette as RGB.
			k := sent.Kelvin
			if govee.HasFrontLight(d.Model) {
				k = 0
			}
			_ = govee.SendColor(d.IP, sent.R, sent.G, sent.B, k)
		}
	}
	// Some controllers keep a level per colour mode; reassert it, as
	// paintScene does.
	_ = govee.SendBrightness(d.IP, level)
	a.mu.Lock()
	if a.lastColor == nil {
		a.lastColor = map[string]color.RGBK{}
	}
	a.lastColor[d.IP] = sent
	a.mu.Unlock()
}
