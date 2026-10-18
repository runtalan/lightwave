package main

import (
	"strings"
	"sync"
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
	}{{1, barBackOff}, {1, "Cyber"}, {1, "Colorful"}, {1, ""}, {-1, "Colorful"}, {4, "Colorful"}, {-2, barBackOff}, {-1, ""}} {
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

// A trimmed bar reporting the level it was given must not drag the slider
// down: each poll would otherwise shave the trim off again.
func TestTrimmedLampReportLeavesSliderBe(t *testing.T) {
	slider := 64
	for i := 0; i < 30; i++ {
		bar := ignitedBrightness(ignitedBrightness(slider) * 92 / 100)
		slider = sliderFromReported(bar, slider, 92)
	}
	if slider != 64 {
		t.Fatalf("slider drifted to %d, want 64", slider)
	}
	// Dimmed to 30 from the Govee app: the slider follows, scaled by the trim.
	if got := sliderFromReported(30, 64, 92); got != 33 {
		t.Fatalf("external change mapped to %d, want 33", got)
	}
	if got := sliderFromReported(40, 64, 100); got != 40 {
		t.Fatalf("untrimmed lamp mapped to %d, want 40", got)
	}
}

// Hidden scenes never reach the dial; the rest keep Govee's order.
func TestHiddenScenesLeaveTheDial(t *testing.T) {
	got := visibleScenes([]govee.Scene{{Name: "Esports"}, {Name: "Split"}, {Name: "Fight"}})
	if len(got) != 2 || got[0].Name != "Esports" || got[1].Name != "Fight" {
		t.Fatalf("visible = %v, want Esports, Fight", got)
	}
}

// With a scene on the bar, the temperature still reaches the front light as
// the dial turns, in any colour mode: at most one write per frontWarmthEvery,
// ending on the last value, then the scene the Kelvin stops starts again once.
func TestFrontWarmthDuringSceneRestartsTheScene(t *testing.T) {
	for _, warm := range []bool{true, false} {
		const bar, bulb = "192.0.2.95", "192.0.2.96"
		a := sceneApp(bar, bulb)
		a.barScene = "Cyber"
		a.warmMode = warm
		var mu sync.Mutex
		sent := map[string][]string{}
		govee.SetTestControlSink(func(ip, p string) {
			mu.Lock()
			sent[ip] = append(sent[ip], p)
			mu.Unlock()
		})

		// A fast spin: twenty detents, 2ms apart.
		start := time.Now()
		for k := 4000; k > 2000; k -= 100 {
			a.SetFrontWarmth(k)
			time.Sleep(2 * time.Millisecond)
		}
		t.Logf("warm=%v: spin took %v", warm, time.Since(start))
		time.Sleep(barSceneSettle + 200*time.Millisecond)
		govee.SetTestControlSink(nil)

		mu.Lock()
		got := sent[bar]
		if len(sent[bulb]) != 0 {
			t.Fatalf("warm=%v: the rest of the room was written to: %v", warm, sent[bulb])
		}
		var kelvins, scenes, lastKelvin, firstScene int
		firstScene = -1
		for i, p := range got {
			if strings.Contains(p, `"colorTemInKelvin"`) {
				kelvins++
				lastKelvin = i
			}
			// A scene ends on its 33 05 04 select frame ("MwUE" in base64);
			// other ptReal writes, such as the back light's switch, are not
			// restarts.
			if strings.Contains(p, `"cmd":"ptReal"`) && strings.Contains(p, `"MwUE`) {
				scenes++
				if firstScene < 0 {
					firstScene = i
				}
			}
		}
		if kelvins < 1 || kelvins > 2 {
			t.Fatalf("warm=%v: %d Kelvin writes for a 40ms spin, want 1 or 2", warm, kelvins)
		}
		if !strings.Contains(got[lastKelvin], `"colorTemInKelvin":2700`) {
			t.Fatalf("warm=%v: last Kelvin write %s, want the dial's final 2700 (clamped)", warm, got[lastKelvin])
		}
		if scenes != 1 || firstScene < lastKelvin {
			t.Fatalf("warm=%v: want one scene restart after the last Kelvin, got %v", warm, got)
		}
		mu.Unlock()
	}
}

// "Back light off" switches only the bar's back light off, sends no scene, and
// keeps the room painters off the bar; leaving it relights the back first.
func TestBarBackOffDarkensOnlyTheBack(t *testing.T) {
	const bar, bulb = "192.0.2.97", "192.0.2.98"
	a := sceneApp(bar, bulb)
	a.barScene = barBackOff
	sent := map[string][]string{}
	govee.SetTestControlSink(func(ip, p string) { sent[ip] = append(sent[ip], p) })
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	// 33 36 01 00 and 33 36 01 01, checksummed, as base64.
	const backOff, backOn = "MzYBAAAAAAAAAAAAAAAAAAAAAAQ=", "MzYBAQAAAAAAAAAAAAAAAAAAAAU="
	a.applyBarScene(true)
	if len(sent[bar]) != 1 || !strings.Contains(sent[bar][0], backOff) {
		t.Fatalf("bar got %v, want only the back light switched off", sent[bar])
	}
	if len(sent[bulb]) != 0 {
		t.Fatalf("the rest of the room was written to: %v", sent[bulb])
	}

	sent = map[string][]string{}
	a.paintScene(false)
	if len(sent[bar]) != 0 || len(sent[bulb]) == 0 {
		t.Fatalf("palette paint: bar=%v bulb=%v; want the bar passed by, the room painted", sent[bar], sent[bulb])
	}

	sent = map[string][]string{}
	a.barScene = "Cyber"
	a.applyBarScene(false)
	if len(sent[bar]) < 2 || !strings.Contains(sent[bar][0], backOn) || !strings.Contains(sent[bar][1], "MwUE") {
		t.Fatalf("leaving Off: bar got %v, want the back light on, then the scene", sent[bar])
	}
	if got := barSceneSlot(barBackOff, nil); got != 1 {
		t.Fatalf("Off is slot %d of the ring, want 1", got)
	}
}
