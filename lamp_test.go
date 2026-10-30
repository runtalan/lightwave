package main

import (
	"strconv"
	"testing"

	"lightwave/internal/config"
)

// Whatever the slider says, the trim LEVEL picks lands the lamp on exactly the
// level asked for, by the same sum every send uses.
func TestTrimForLevelLandsExactly(t *testing.T) {
	for slider := 0; slider <= 100; slider++ {
		for level := 1; level <= 100; level++ {
			trim := trimForLevel(level, slider)
			got := ignitedBrightness(ignitedBrightness(slider) * trim / 100)
			if got != level {
				t.Fatalf("slider %d level %d: trim %d gives %d", slider, level, trim, got)
			}
		}
	}
}

func TestTrimForLevelAtSliderIsUntrimmed(t *testing.T) {
	if got := trimForLevel(47, 47); got != 100 {
		t.Fatalf("got trim %d, want 100", got)
	}
}

// A lamp boosted above the slider reporting what it was sent must not drag the
// slider to its own level, and topping out at full says nothing either.
func TestSliderFromReportedBoosted(t *testing.T) {
	if got := sliderFromReported(80, 47, trimForLevel(80, 47)); got != 47 {
		t.Fatalf("own send read back: slider %d, want 47", got)
	}
	if got := sliderFromReported(100, 60, 300); got != 60 {
		t.Fatalf("clamped at full: slider %d, want 60", got)
	}
}

func nudgeTestApp(slider int, trims ...int) *App {
	a := &App{pool: map[int]bool{}, brightness: slider, brightKick: make(chan struct{}, 1)}
	a.slots = make([]config.SlotBinding, 9)
	for i := range a.slots {
		a.slots[i] = config.SlotBinding{Slot: i + 1}
	}
	for i, trim := range trims {
		a.slots[i].DeviceID = "LAMP" + strconv.Itoa(i+1)
		a.slots[i].Trim = trim
	}
	return a
}

func levels(a *App, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = a.slotLevelLocked(i + 1)
	}
	return out
}

// A turn of the brightness dial moves every lamp by the same number of points,
// so a lamp set below the room stays the same distance below it.
func TestNudgeBrightnessMovesEveryLampEqually(t *testing.T) {
	a := nudgeTestApp(80, 0, 50) // 80% and 40%
	a.NudgeBrightness(10)
	if got := levels(a, 2); got[0] != 90 || got[1] != 50 {
		t.Fatalf("after +10: levels %v, want [90 50]", got)
	}
	a.NudgeBrightness(-30)
	if got := levels(a, 2); got[0] != 60 || got[1] != 20 {
		t.Fatalf("after -30: levels %v, want [60 20]", got)
	}
	if a.slots[0].Trim != 0 {
		t.Fatalf("lamp level with the slider kept trim %d", a.slots[0].Trim)
	}
}

// At the top and bottom each lamp stops on its own; the rest keep moving.
func TestNudgeBrightnessClampsPerLamp(t *testing.T) {
	a := nudgeTestApp(100, 0, 90) // 100% and 90%
	a.NudgeBrightness(6)
	if got := levels(a, 2); got[0] != 100 || got[1] != 96 {
		t.Fatalf("after +6: levels %v, want [100 96]", got)
	}
	a = nudgeTestApp(10, 0, 30) // 10% and 3%
	a.NudgeBrightness(-8)
	if got := levels(a, 2); got[0] != 2 || got[1] != 1 {
		t.Fatalf("after -8: levels %v, want [2 1]", got)
	}
}

// The slider is absolute: it puts every lamp on its own level.
func TestSetBrightnessClearsTrims(t *testing.T) {
	a := nudgeTestApp(80, 0, 50, 140)
	a.SetBrightness(100)
	if got := levels(a, 3); got[0] != 100 || got[1] != 100 || got[2] != 100 {
		t.Fatalf("slider at 100: levels %v, want all 100", got)
	}
}
