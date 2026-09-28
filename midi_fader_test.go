package main

import "testing"

// A controller that keeps re-sending a parked fader's value must not count as
// fresh input each time, or every poll re-pushes full state and the fader
// overrides every other brightness source.
func TestNewFaderReadingDropsRepeats(t *testing.T) {
	a := &App{}
	steps := []struct {
		cc, val uint8
		want    bool
	}{
		{62, 40, true},  // first reading always applies
		{62, 40, false}, // parked fader re-sent
		{62, 40, false},
		{62, 41, true}, // real movement
		{62, 40, true}, // moving back is movement too
		{7, 40, true},  // same value on the alternate CC is a different control
		{62, 0, true},  // bottom of the fader is a reading, not "none"
		{62, 0, false},
	}
	for i, s := range steps {
		if got := a.newFaderReading(s.cc, s.val); got != s.want {
			t.Fatalf("step %d (cc %d val %d): got %v, want %v", i, s.cc, s.val, got, s.want)
		}
	}

	// startMIDI clears the memory so a reconnected port's first reading applies.
	a.midiLastCC.Store(0)
	if !a.newFaderReading(62, 0) {
		t.Fatal("first reading after reset was dropped")
	}
}
