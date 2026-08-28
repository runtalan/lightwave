package midi

import "testing"

// A fader that never reaches 127 must still be able to drive brightness to
// 100. Before calibration a controller topping out at 117 mapped to 92% and
// the light could not be driven to full from MIDI at all.
func TestFullThrowReaches100OnShortFader(t *testing.T) {
	resetCCMax(t)
	SetCCRange(0, 117) // calibrated: this fader really tops out at 117
	if got := CCToPercent(117); got != 100 {
		t.Fatalf("full throw on a 117-max fader = %d%%, want 100%%", got)
	}
	if got := CCToPercent(0); got != 1 {
		t.Fatalf("fader bottom = %d%%, want 1%%", got)
	}
	if mid := CCToPercent(59); mid < 45 || mid > 55 {
		t.Fatalf("mid throw = %d%%, want roughly 50%%", mid)
	}
}

// A fader whose bottom rests above 0 must still reach 1% at its own floor.
func TestCalibratedFloorReaches1(t *testing.T) {
	resetCCMax(t)
	SetCCRange(12, 117)
	if got := CCToPercent(12); got != 1 {
		t.Fatalf("calibrated floor = %d%%, want 1%%", got)
	}
	if got := CCToPercent(5); got != 1 {
		t.Fatalf("below floor = %d%%, want 1%%", got)
	}
	if got := CCToPercent(117); got != 100 {
		t.Fatalf("calibrated ceiling = %d%%, want 100%%", got)
	}
}

// An inverted or collapsed range must fall back to the full span rather than
// making every fader move meaningless.
func TestInvertedRangeFallsBack(t *testing.T) {
	resetCCMax(t)
	SetCCRange(100, 20)
	if lo, hi := CCRange(); lo != 0 || hi != 127 {
		t.Fatalf("inverted range kept: %d..%d", lo, hi)
	}
	if got := CCToPercent(127); got != 100 {
		t.Fatalf("after fallback CC 127 = %d%%, want 100%%", got)
	}
}

// A standard 0-127 fader must be unaffected.
func TestFullRangeFaderUnchanged(t *testing.T) {
	resetCCMax(t)
	SetCCRange(0, 127)
	if got := CCToPercent(127); got != 100 {
		t.Fatalf("CC 127 = %d%%, want 100%%", got)
	}
	if got := CCToPercent(64); got < 48 || got > 52 {
		t.Fatalf("CC 64 = %d%%, want roughly 50%%", got)
	}
}

func resetCCMax(t *testing.T) {
	t.Helper()
	ccRange.Store(defaultCCRange)
}
