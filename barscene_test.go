package main

import (
	"strings"
	"testing"

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
