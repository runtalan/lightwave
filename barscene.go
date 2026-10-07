package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lightwave/internal/config"
	"lightwave/internal/govee"
)

// Govee scenes on the monitor bar's back light.
//
// A lamp with a front light (govee.HasFrontLight) can play one of the effects
// Govee's app offers for its model on its colour LEDs. While barScene names a
// scene in the loaded library the room painters (paintScene, paintWarmness, the fade) leave those LEDs to
// the scene; the front light and the brightness slider still reach the lamp.

// barSceneSettle is how long a dial has to rest on a scene before it is sent.
// Each scene is a burst of frames, and spinning past a dozen of them should
// not queue a dozen bursts on the lamp.
const barSceneSettle = 250 * time.Millisecond

// sceneRetry spaces out library downloads after one fails, so a dial turned
// while offline does not wait on the network every detent.
const sceneRetry = time.Minute

// frontModelLocked is the model of the first bound lamp with a front light,
// or "". Caller holds a.mu.
func (a *App) frontModelLocked() string {
	for _, s := range a.slots {
		if s.DeviceID != "" && govee.HasFrontLight(s.Model) {
			return strings.ToUpper(strings.TrimSpace(s.Model))
		}
	}
	return ""
}

// barSceneLocked looks up the scene barScene names. Caller holds a.mu.
func (a *App) barSceneLocked() (govee.Scene, bool) {
	if a.barScene == "" {
		return govee.Scene{}, false
	}
	for _, sc := range a.sceneLib[a.frontModelLocked()] {
		if sc.Name == a.barScene {
			return sc, true
		}
	}
	return govee.Scene{}, false
}

func sceneCachePath(model string) string {
	dir, err := config.ConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "scenes-"+model+".json")
}

// barScenes returns the scene library for the bound front-light lamp. The
// copy on disk serves until fresh asks for a download; a failed download
// falls back to it.
func (a *App) barScenes(fresh bool) []govee.Scene {
	a.mu.Lock()
	model := a.frontModelLocked()
	lib, have := a.sceneLib[model]
	failed := a.sceneFailedAt
	a.mu.Unlock()
	if model == "" || (have && !fresh) {
		return lib
	}
	path := sceneCachePath(model)
	var body []byte
	if !fresh && path != "" {
		body, _ = os.ReadFile(path)
	}
	if body == nil && (fresh || time.Since(failed) > sceneRetry) {
		b, err := govee.FetchSceneLibrary(model)
		if err != nil {
			log.Printf("scenes %s: %v", model, err)
			a.mu.Lock()
			a.sceneFailedAt = time.Now()
			a.mu.Unlock()
		} else {
			body = b
			if path != "" {
				if err := os.WriteFile(path, b, 0o600); err != nil {
					log.Printf("scenes %s: cache: %v", model, err)
				}
			}
		}
	}
	if body == nil && path != "" {
		body, _ = os.ReadFile(path)
	}
	if body == nil {
		return lib
	}
	parsed, err := govee.ParseScenes(body)
	if err != nil {
		log.Printf("scenes %s: %v", model, err)
		return lib
	}
	a.mu.Lock()
	if a.sceneLib == nil {
		a.sceneLib = map[string][]govee.Scene{}
	}
	a.sceneLib[model] = parsed
	a.mu.Unlock()
	return parsed
}

// StepBarScene moves the bar through its scenes, steps at a time. The ring
// holds the room's own colours first, then every scene in Govee's order.
func (a *App) StepBarScene(steps int) (HUDState, error) {
	lib := a.barScenes(false)
	if len(lib) == 0 {
		return a.snapshot(), fmt.Errorf("no scenes for the monitor bar")
	}
	a.mu.Lock()
	cur := 0
	for i, sc := range lib {
		if sc.Name == a.barScene {
			cur = i + 1
			break
		}
	}
	a.mu.Unlock()
	ring := len(lib) + 1
	next := ((cur+steps)%ring + ring) % ring
	name := ""
	if next > 0 {
		name = lib[next-1].Name
	}
	return a.SetBarScene(name), nil
}

// SelectBarScene plays the scene with this name, in any case. An unknown
// name is an error; use SetBarScene("") to go back to the room's colours.
func (a *App) SelectBarScene(name string) (HUDState, error) {
	for _, sc := range a.barScenes(false) {
		if strings.EqualFold(sc.Name, strings.TrimSpace(name)) {
			return a.SetBarScene(sc.Name), nil
		}
	}
	return a.snapshot(), fmt.Errorf("no scene called %q", name)
}

// SetBarScene plays a scene on the bar's back light, or with "" hands it back
// to the room's colours. The lamp hears about it once the choice settles.
func (a *App) SetBarScene(name string) HUDState {
	a.recordUserActivity()
	a.mu.Lock()
	if name == a.barScene {
		a.mu.Unlock()
		return a.snapshot()
	}
	a.barScene = name
	s := a.settings
	s.BarScene = name
	a.settings = s
	a.barSceneGen++
	gen := a.barSceneGen
	a.mu.Unlock()
	if err := config.SaveSettings(s); err != nil {
		log.Printf("persist bar scene: %v", err)
	}
	time.AfterFunc(barSceneSettle, func() {
		a.mu.Lock()
		current := gen == a.barSceneGen
		a.mu.Unlock()
		if current {
			a.applyBarScene(true)
		}
	})
	a.emitState()
	return a.snapshot()
}

// applyBarScene sends the bar's scene to every pooled lamp with a front
// light. With no scene chosen it does nothing, unless restore asks for the
// room's colours to be painted back.
func (a *App) applyBarScene(restore bool) {
	a.mu.Lock()
	var front []lampDest
	for _, d := range a.poolDestsLocked() {
		if govee.HasFrontLight(d.Model) {
			front = append(front, d)
		}
	}
	sc, ok := a.barSceneLocked()
	brightness := ignitedBrightness(a.brightness)
	warm, dancing := a.warmMode, a.dancing
	a.mu.Unlock()
	if len(front) == 0 {
		return
	}
	if !ok {
		// The fade picks the bar back up on its own next tick.
		if !restore || dancing {
			return
		}
		if warm {
			a.paintWarmness(false)
			return
		}
		a.sendFrontWarmth()
		a.paintSceneOnly(false, func(d lampDest) bool { return govee.HasFrontLight(d.Model) })
		return
	}
	for _, d := range front {
		_ = govee.SendScene(d.IP, sc)
		// A scene can carry a level of its own; keep the slider's.
		level := brightness
		if d.Trim > 0 {
			level = ignitedBrightness(brightness * d.Trim / 100)
		}
		_ = govee.SendBrightness(d.IP, level)
	}
}
