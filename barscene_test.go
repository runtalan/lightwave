package main

import (
	"strings"
	"testing"
	"time"

	"lightwave/internal/color"
	"lightwave/internal/config"
	"lightwave/internal/govee"
)

func sceneApp(bar, bulb string) *App {
	return &App{
		pool: map[int]bool{1: true, 2: true},
		slots: []config.SlotBinding{
			{Slot: 1, DeviceID: "BAR", IP: bar, Model: "H2800"},
			{Slot: 2, DeviceID: "BULB", IP: bulb, Model: "H6001"},
		},
		lastColor: map[string]color.RGBK{},
		sceneLib: map[string][]govee.Scene{"H2800": {
			{Name: "Cyber", Code: 33285, Param: []byte{1, 2, 3}},
			{Name: "Colorful", Code: 33286},
		}},
	}
}

// The dial walks room colours, then every scene, and wraps both ways.
func TestStepBarSceneWrapsThroughTheRoom(t *testing.T) {
	a := sceneApp("192.0.2.81", "192.0.2.82")
	// Retire the settle timers each step started, so none fires into a
	// later test's control sink.
	t.Cleanup(func() {
		a.mu.Lock()
		a.barSceneGen++
		a.mu.Unlock()
	})
	for _, c := range []struct {
		steps int
		want  string
	}{{1, "Cyber"}, {1, "Colorful"}, {1, ""}, {-1, "Colorful"}, {6, "Colorful"}, {-2, ""}} {
		if _, err := a.StepBarScene(c.steps); err != nil {
			t.Fatal(err)
		}
		if a.barScene != c.want {
			t.Fatalf("after %+d: scene = %q, want %q", c.steps, a.barScene, c.want)
		}
	}
	if _, err := a.SelectBarScene("colorful"); err != nil || a.barScene != "Colorful" {
		t.Fatalf("select by name: scene=%q err=%v", a.barScene, err)
	}
	if _, err := a.SelectBarScene("Nope"); err == nil {
		t.Fatal("an unknown scene name must be an error")
	}
}

// While a scene plays, palette paints pass the bar by and the scene reaches
// only the bar.
func TestBarSceneHoldsAgainstPalettePaint(t *testing.T) {
	const bar, bulb = "192.0.2.83", "192.0.2.84"
	a := sceneApp(bar, bulb)
	a.barScene = "Cyber"
	sent := map[string][]string{}
	govee.SetTestControlSink(func(ip, p string) { sent[ip] = append(sent[ip], p) })
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	a.paintScene(false)
	if len(sent[bar]) != 0 {
		t.Fatalf("palette paint reached the bar during a scene: %v", sent[bar])
	}
	if len(sent[bulb]) == 0 {
		t.Fatal("palette paint skipped the rest of the room")
	}

	sent = map[string][]string{}
	a.applyBarScene(false)
	if len(sent[bulb]) != 0 {
		t.Fatalf("scene reached a lamp with no front light: %v", sent[bulb])
	}
	if len(sent[bar]) == 0 || !strings.Contains(sent[bar][0], `"cmd":"ptReal"`) {
		t.Fatalf("scene must go to the bar as ptReal, got %v", sent[bar])
	}
}

// Trimming the bar while the slider is parked dims the bar alone; the rest of
// the pool keeps the level it already has.
func TestTrimResendsOnlyTheTrimmedLamp(t *testing.T) {
	const bar, bulb = "192.0.2.85", "192.0.2.86"
	a := sceneApp(bar, bulb)
	a.slots[0].Trim = 40
	a.stop = make(chan struct{})
	a.brightKick = make(chan struct{}, 1)
	a.pendingBright.Store(80)
	a.lastSentBright = ignitedBrightness(80)
	a.trimDirty = map[string]bool{bar: true}

	got := make(chan map[string]int, 1)
	sendMu.Lock()
	prev := sendPoolFunc
	sendPoolFunc = func(targets map[string]int, _ bool) { got <- targets }
	sendMu.Unlock()
	t.Cleanup(func() {
		close(a.stop)
		sendMu.Lock()
		sendPoolFunc = prev
		sendMu.Unlock()
	})

	go a.brightnessPump()
	a.brightKick <- struct{}{}
	select {
	case targets := <-got:
		if _, ok := targets[bulb]; ok || len(targets) != 1 {
			t.Fatalf("trim sent to %v, want the bar alone", targets)
		}
		if want := ignitedBrightness(ignitedBrightness(80) * 40 / 100); targets[bar] != want {
			t.Fatalf("bar = %d, want %d", targets[bar], want)
		}
	case <-time.After(time.Second):
		t.Fatal("trim never reached the bar")
	}
}

// Turning the scene dial back to the room's colours in Warmness mode repaints
// the bar alone.
func TestBarSceneRestoreInWarmnessTouchesOnlyTheBar(t *testing.T) {
	const bar, bulb = "192.0.2.87", "192.0.2.88"
	a := sceneApp(bar, bulb)
	a.warmMode = true
	a.warmness = 3000
	sent := map[string][]string{}
	govee.SetTestControlSink(func(ip, p string) { sent[ip] = append(sent[ip], p) })
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	a.applyBarScene(true)
	if len(sent[bulb]) != 0 {
		t.Fatalf("scene restore reached the rest of the room: %v", sent[bulb])
	}
	if len(sent[bar]) == 0 {
		t.Fatal("scene restore never repainted the bar")
	}
}

// In Warmness mode the bar keeps its own temperature: setting it repaints the
// bar alone, and a room-wide warmness paint still gives the bar its own.
func TestFrontWarmthInWarmnessIsTheBarsOwn(t *testing.T) {
	const bar, bulb = "192.0.2.89", "192.0.2.90"
	a := sceneApp(bar, bulb)
	a.warmMode = true
	a.warmness = 2200
	a.frontWarmth = 5000
	sent := map[string][]string{}
	govee.SetTestControlSink(func(ip, p string) { sent[ip] = append(sent[ip], p) })
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	a.paintWarmness(false)
	if !strings.Contains(sent[bar][0], `"colorTemInKelvin":5000`) {
		t.Fatalf("bar painted %v, want its own 5000K", sent[bar])
	}
	if !strings.Contains(sent[bulb][0], `"colorTemInKelvin":2200`) {
		t.Fatalf("bulb painted %v, want the room's 2200K", sent[bulb])
	}

	sent = map[string][]string{}
	a.paintWarmnessOnly(false, func(d lampDest) bool { return govee.HasFrontLight(d.Model) })
	if len(sent[bulb]) != 0 || len(sent[bar]) == 0 {
		t.Fatalf("bar-only paint: bar=%v bulb=%v", sent[bar], sent[bulb])
	}
}
