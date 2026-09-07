package main

import (
	"encoding/json"
	"strings"
	"testing"

	"lightwave/internal/color"
	"lightwave/internal/config"
	"lightwave/internal/govee"
)

// kelvinOf returns the colorTemInKelvin of the last colorwc payload, or -1.
func kelvinOf(payloads []string) int {
	out := -1
	for _, p := range payloads {
		if !strings.Contains(p, `"cmd":"colorwc"`) {
			continue
		}
		var m struct {
			Msg struct {
				Data struct {
					K int `json:"colorTemInKelvin"`
				} `json:"data"`
			} `json:"msg"`
		}
		if json.Unmarshal([]byte(p), &m) == nil {
			out = m.Msg.Data.K
		}
	}
	return out
}

// A Kelvin palette must come back to full brightness when gradient is turned
// off again. The white diodes are what make these palettes bright, and they
// only light when colorTemInKelvin > 0. Toggling into gradient sends RGB-only
// frames; toggling back out has to restore the temperature, otherwise the pool
// stays visibly dim until the user cycles palettes to jolt it out of it.
func TestGradientToggleRestoresKelvin(t *testing.T) {
	// Pick a palette whose swatches all carry a colour temperature.
	idx := -1
	for i, p := range color.Palettes {
		if len(p.Colors) == 0 {
			continue
		}
		all := true
		for _, c := range p.Colors {
			if c.Kelvin <= 0 {
				all = false
				break
			}
		}
		if all {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Skip("no all-Kelvin palette to exercise")
	}

	const ip = "192.0.2.90"
	a := &App{
		pool:      map[int]bool{1: true},
		slots:     []config.SlotBinding{{Slot: 1, IP: ip, Model: "H6001"}},
		lastColor: map[string]color.RGBK{},
	}
	a.engine.Index = idx

	var payloads []string
	govee.SetTestControlSink(func(_, p string) { payloads = append(payloads, p) })
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	// Single -> establishes the bright, Kelvin-bearing baseline.
	a.paintScene(true)
	baseline := kelvinOf(payloads)
	if baseline <= 0 {
		t.Fatalf("single mode sent no Kelvin (got %d)", baseline)
	}

	// Into gradient: RGB only, by design.
	a.gradient = true
	payloads = nil
	a.paintScene(true)

	// Back to single: the temperature must return, or the lamp stays dim.
	a.gradient = false
	payloads = nil
	a.paintScene(true)
	if got := kelvinOf(payloads); got != baseline {
		t.Fatalf("after gradient toggle Kelvin = %d, want %d (lamp stays dim)", got, baseline)
	}
}

// brightnessOf returns the values of every brightness payload sent to ip.
func brightnessOf(payloads []string) []int {
	var out []int
	for _, p := range payloads {
		if !strings.Contains(p, `"cmd":"brightness"`) {
			continue
		}
		var m struct {
			Msg struct {
				Data struct {
					V int `json:"value"`
				} `json:"data"`
			} `json:"msg"`
		}
		if json.Unmarshal([]byte(p), &m) == nil {
			out = append(out, m.Msg.Data.V)
		}
	}
	return out
}

// Painting a palette moves every lamp into a colour mode, and some BLE RGBIC
// controllers keep a separate level per mode — so the lamp adopts whatever it
// had stored there instead of the slider. paintWarmness already reasserts the
// level; paintScene must too, or a slider sitting at 100% is never re-sent (the
// pump skips it as unchanged) and the pool stays visibly dim.
func TestPaintSceneReassertsBrightness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		model    string
		gradient bool
	}{
		{"single colour", "H6001", false},
		{"segment gradient", "H617A", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const ip = "192.0.2.91"
			a := &App{
				pool:       map[int]bool{1: true},
				slots:      []config.SlotBinding{{Slot: 1, IP: ip, Model: tc.model}},
				lastColor:  map[string]color.RGBK{},
				brightness: 100,
				gradient:   tc.gradient,
			}
			govee.Remember(ip, tc.model)

			var payloads []string
			govee.SetTestControlSink(func(_, p string) { payloads = append(payloads, p) })
			t.Cleanup(func() { govee.SetTestControlSink(nil) })

			a.paintScene(true)

			got := brightnessOf(payloads)
			if len(got) == 0 {
				t.Fatalf("paintScene sent no brightness — lamp keeps its per-mode level and stays dim")
			}
			for _, v := range got {
				if v != 100 {
					t.Fatalf("reasserted brightness = %d, want 100", v)
				}
			}
		})
	}
}

// The pump skips a send when the level has not changed. A palette repaint has
// to clear that latch, otherwise the next slider move at the same percentage
// never reaches lamps that just adopted a per-mode level.
func TestPaintSceneClearsBrightnessLatch(t *testing.T) {
	const ip = "192.0.2.92"
	a := &App{
		pool:           map[int]bool{1: true},
		slots:          []config.SlotBinding{{Slot: 1, IP: ip, Model: "H6001"}},
		lastColor:      map[string]color.RGBK{},
		brightness:     100,
		lastSentBright: 100,
	}
	govee.SetTestControlSink(func(_, _ string) {})
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	a.paintScene(true)

	if a.lastSentBright != -1 {
		t.Fatalf("lastSentBright = %d after repaint, want -1 so the pump re-sends", a.lastSentBright)
	}
}
