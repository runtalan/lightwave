package main

import (
	"testing"

	"lightwave-sd/internal/lw"
)

// Only lights that are on are stepped through: pads 3 and 7 are bound but off.
func TestNextLightWalksLitPadsAndWraps(t *testing.T) {
	st := pads(map[int]bool{2: true, 3: false, 5: true, 7: false, 9: true})
	for _, c := range []struct{ cur, want int }{{2, 5}, {5, 9}, {9, 2}} {
		if got := nextLight(st, c.cur); got != c.want {
			t.Errorf("nextLight(%d) = %d, want %d", c.cur, got, c.want)
		}
	}
}

// A fresh dial shows the first lit light, and its first push moves on from it.
func TestNextLightFromNone(t *testing.T) {
	st := pads(map[int]bool{1: false, 2: true, 5: true})
	if p := selectedLight(st, 0); p == nil || p.Number != 2 {
		t.Errorf("fresh dial: got %+v, want pad 2", p)
	}
	if got := nextLight(st, 0); got != 5 {
		t.Errorf("first push: got %d, want 5", got)
	}
}

// When the light the dials are on goes off they move to the next lit one, and
// a push from there carries on in pad order.
func TestSelectedLightSkipsALightTurnedOff(t *testing.T) {
	st := pads(map[int]bool{2: true, 5: false, 8: true})
	if p := selectedLight(st, 5); p == nil || p.Number != 8 {
		t.Errorf("got %+v, want pad 8", p)
	}
	if got := nextLight(st, 5); got != 2 {
		t.Errorf("push: got %d, want 2", got)
	}
	// Wraps past the last pad.
	st = pads(map[int]bool{2: true, 9: false})
	if p := selectedLight(st, 9); p == nil || p.Number != 2 {
		t.Errorf("wrap: got %+v, want pad 2", p)
	}
}

func TestNextLightNothingLit(t *testing.T) {
	st := pads(map[int]bool{1: false, 4: false})
	if got := nextLight(st, 1); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	if selectedLight(st, 1) != nil {
		t.Errorf("selectedLight with nothing lit should be nil")
	}
	if selectedLight(pads(nil), 3) != nil {
		t.Errorf("selectedLight with nothing bound should be nil")
	}
}

func TestLightLevelCmd(t *testing.T) {
	st := lw.State{Brightness: 47}
	pad := func(level int) *lw.Pad { return &lw.Pad{Number: 3, Bound: true, Level: level} }
	for _, c := range []struct {
		level, ticks int
		want         string
	}{
		{60, 1, "LEVEL 3 62"},
		{60, -3, "LEVEL 3 54"},
		// Past the main slider: a light can be brighter than the room.
		{47, 10, "LEVEL 3 67"},
		{99, 5, "LEVEL 3 100"},
		{3, -10, "LEVEL 3 1"},
	} {
		got, ok := lightLevelCmd(st, pad(c.level), c.ticks)
		if !ok || got != c.want {
			t.Errorf("level %d ticks %d: got %q ok=%v, want %q", c.level, c.ticks, got, ok, c.want)
		}
	}
	if cmd, ok := lightLevelCmd(st, pad(50), 0); !ok || cmd != "" {
		t.Errorf("zero ticks: got %q ok=%v, want empty cmd", cmd, ok)
	}
	if _, ok := lightLevelCmd(st, nil, 1); ok {
		t.Errorf("no light should not be ok")
	}
}

// Without a level from Lightwave the dial works it out from the slider.
func TestLightLevelFallsBackToTrim(t *testing.T) {
	st := lw.State{Brightness: 50}
	if got := lightLevel(st, &lw.Pad{Trim: 60}); got != 30 {
		t.Errorf("got %d, want 30", got)
	}
	if got := lightLevel(st, &lw.Pad{}); got != 50 {
		t.Errorf("untrimmed: got %d, want 50", got)
	}
}

func colorState() lw.State {
	return lw.State{
		Palette:  "B",
		Palettes: []string{"A", "B", "C"},
		Warmness: 3000,
	}
}

func TestLightColorCmd(t *testing.T) {
	st := colorState()
	for _, c := range []struct {
		name  string
		pad   lw.Pad
		warm  bool
		ticks int
		want  string
	}{
		{"room palette starts one on from the room", lw.Pad{Number: 2}, false, 1, "LIGHT_COLOR 2 PALETTE 2"},
		{"room palette backwards", lw.Pad{Number: 2}, false, -1, "LIGHT_COLOR 2 PALETTE 0"},
		{"room warmness starts from the room's white", lw.Pad{Number: 2}, true, 1, "LIGHT_COLOR 2 WARM 2900"},
		{"own palette wraps", lw.Pad{Number: 2, Color: lw.LampPalette, Palette: "C"}, false, 1, "LIGHT_COLOR 2 PALETTE 0"},
		{"own palette by name in any case", lw.Pad{Number: 2, Color: lw.LampPalette, Palette: "a"}, false, -1, "LIGHT_COLOR 2 PALETTE 2"},
		{"own white, clockwise warmer", lw.Pad{Number: 2, Color: lw.LampWarm, Kelvin: 2700}, false, 2, "LIGHT_COLOR 2 WARM 2500"},
		{"own white stays in range", lw.Pad{Number: 2, Color: lw.LampWarm, Kelvin: 2100}, false, 5, "LIGHT_COLOR 2 WARM 2000"},
	} {
		st.WarmMode = c.warm
		pad := c.pad
		got, ok := lightColorCmd(st, &pad, c.ticks)
		if !ok || got != c.want {
			t.Errorf("%s: got %q ok=%v, want %q", c.name, got, ok, c.want)
		}
	}
	if _, ok := lightColorCmd(st, nil, 1); ok {
		t.Errorf("no light should not be ok")
	}
}

// A tap walks room -> own palette -> own white -> room.
func TestLightColorTapCycles(t *testing.T) {
	st := colorState()
	for _, c := range []struct {
		pad  lw.Pad
		want string
	}{
		{lw.Pad{Number: 4}, "LIGHT_COLOR 4 PALETTE 1"},
		{lw.Pad{Number: 4, Color: lw.LampPalette, Palette: "C"}, "LIGHT_COLOR 4 WARM 3000"},
		{lw.Pad{Number: 4, Color: lw.LampWarm, Kelvin: 2700}, "LIGHT_COLOR 4 ROOM"},
	} {
		pad := c.pad
		if got := lightColorTapCmd(st, &pad); got != c.want {
			t.Errorf("from %q: got %q, want %q", c.pad.Color, got, c.want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("Desk", 14); got != "Desk" {
		t.Errorf("got %q", got)
	}
	if got := clip("ClaudiaBulbH6001-C883", 14); got != "ClaudiaBulbH6…" {
		t.Errorf("got %q", got)
	}
}

func TestAllLevelsSpansLitLights(t *testing.T) {
	st := lw.State{Brightness: 80, Pads: []lw.Pad{
		{Number: 1, Bound: true, On: true, Level: 80},
		{Number: 2, Bound: true, On: true, Level: 45},
		{Number: 3, Bound: true, On: false, Level: 10},
	}}
	if lo, hi := allLevels(st); lo != 45 || hi != 80 {
		t.Fatalf("got %d-%d, want 45-80", lo, hi)
	}
	if lo, hi := allLevels(lw.State{Brightness: 60}); lo != 60 || hi != 60 {
		t.Fatalf("nothing on: got %d-%d, want the slider's 60", lo, hi)
	}
}

// Tapping the brightness dial walks the lit lights in pad order, then All.
func TestNextBrightnessPickCyclesLitLightsThenAll(t *testing.T) {
	st := lw.State{Pads: []lw.Pad{
		{Number: 2, Bound: true, On: true},
		{Number: 4, Bound: true, On: false},
		{Number: 7, Bound: true, On: true},
	}}
	want := []int{2, 7, 0, 2}
	pick := 0
	for i, w := range want {
		pick = nextBrightnessPick(st, pick)
		if pick != w {
			t.Fatalf("tap %d: on pad %d, want %d", i+1, pick, w)
		}
	}
	// A light switched off while picked reads as All, so the next tap starts over.
	if got := nextBrightnessPick(st, 4); got != 2 {
		t.Fatalf("from an unlit pick: got %d, want 2", got)
	}
	if brightnessPick(st, 4) != nil {
		t.Fatal("an unlit light must not be picked")
	}
	if got := nextBrightnessPick(lw.State{}, 0); got != 0 {
		t.Fatalf("nothing on: got %d, want All", got)
	}
}
