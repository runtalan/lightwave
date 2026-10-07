package main

import (
	"strings"
	"testing"

	"lightwave/internal/color"
	"lightwave/internal/config"
	"lightwave/internal/govee"
)

func TestWarmRGBKeepsCoolWhiteBrightForBLE(t *testing.T) {
	c := warmRGB(6000)
	if c.R != 255 || c.G < 240 || c.B < 230 {
		t.Fatalf("6000K = %#v; want a near-full-output cool white", c)
	}
	if c.Kelvin != 6000 {
		t.Fatalf("Kelvin = %d, want 6000", c.Kelvin)
	}
}

func TestWarmRGBWarmsAsKelvinFalls(t *testing.T) {
	warm, cool := warmRGB(2000), warmRGB(6000)
	if warm.B >= cool.B || warm.G >= cool.G {
		t.Fatalf("2000K %#v should be warmer than 6000K %#v", warm, cool)
	}
}

// The H2800's white front light answers only to a Kelvin write, so its slider
// has to reach it from palette mode, hand the colour LEDs their colour back,
// and leave every other lamp alone.
func TestSetFrontWarmthReachesOnlyFrontLights(t *testing.T) {
	const bar, bulb = "192.0.2.91", "192.0.2.92"
	a := &App{
		pool: map[int]bool{1: true, 2: true},
		slots: []config.SlotBinding{
			{Slot: 1, IP: bar, Model: "H2800"},
			{Slot: 2, IP: bulb, Model: "H6001"},
		},
		lastColor: map[string]color.RGBK{},
	}
	sent := map[string][]string{}
	govee.SetTestControlSink(func(ip, p string) { sent[ip] = append(sent[ip], p) })
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	st := a.SetFrontWarmth(9000)
	if st.FrontWarmth != config.MaxFrontWarmth || !st.FrontLight {
		t.Fatalf("state = %dK frontLight=%v, want clamped to %dK and shown", st.FrontWarmth, st.FrontLight, config.MaxFrontWarmth)
	}
	if len(sent[bulb]) != 0 {
		t.Fatalf("lamp without a front light was written to: %v", sent[bulb])
	}
	got := sent[bar]
	if len(got) < 2 || !strings.Contains(got[0], `"colorTemInKelvin":6500`) {
		t.Fatalf("first write must be the front Kelvin, got %v", got)
	}
	if kelvinOf(got) != 0 {
		t.Fatalf("colour repaint must follow as RGB, last colorwc Kelvin = %d", kelvinOf(got))
	}
}

// A Kelvin palette in solid mode must not overwrite the front light.
func TestPaletteKeepsFrontLightKelvin(t *testing.T) {
	const bar = "192.0.2.93"
	a := &App{
		pool:      map[int]bool{1: true},
		slots:     []config.SlotBinding{{Slot: 1, IP: bar, Model: "H2800"}},
		lastColor: map[string]color.RGBK{},
	}
	var payloads []string
	govee.SetTestControlSink(func(_, p string) { payloads = append(payloads, p) })
	t.Cleanup(func() { govee.SetTestControlSink(nil) })

	// Palette 0 is Warm Whites: every swatch carries a Kelvin.
	a.paintScene(true)
	if got := kelvinOf(payloads); got != 0 {
		t.Fatalf("palette paint sent %dK to a front-light lamp, want RGB only", got)
	}
}
