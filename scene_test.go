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
