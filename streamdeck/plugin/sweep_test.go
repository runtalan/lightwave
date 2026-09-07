package main

import "testing"

import "lightwave-sd/internal/lw"

// pads builds a state from a pad-number -> lit map. Unlisted pads are unbound,
// which is the case that matters: the knob walks bound lights, not pad slots.
func pads(lit map[int]bool) lw.State {
	var st lw.State
	for n := 1; n <= 9; n++ {
		on, bound := lit[n]
		st.Pads = append(st.Pads, lw.Pad{Number: n, Bound: bound, On: on})
	}
	return st
}

func TestSweepStepFillsInPadOrder(t *testing.T) {
	st := pads(map[int]bool{1: false, 2: false, 3: false})
	for _, want := range []int{1, 2, 3} {
		pad, on, done := sweepStep(st, 3)
		if done || !on || pad != want {
			t.Fatalf("got pad=%d on=%v done=%v, want pad=%d on", pad, on, done, want)
		}
		st.Pad(pad).On = true
	}
	if _, _, done := sweepStep(st, 3); !done {
		t.Fatalf("target reached but sweep not done")
	}
}

func TestSweepStepEmptiesInReverse(t *testing.T) {
	st := pads(map[int]bool{1: true, 2: true, 3: true})
	for _, want := range []int{3, 2, 1} {
		pad, on, done := sweepStep(st, 0)
		if done || on || pad != want {
			t.Fatalf("got pad=%d on=%v done=%v, want pad=%d off", pad, on, done, want)
		}
		st.Pad(pad).On = false
	}
	if _, _, done := sweepStep(st, 0); !done {
		t.Fatalf("all off but sweep not done")
	}
}

// Unbound pads are skipped, so a gap in the pad grid does not stall the walk.
func TestSweepStepSkipsUnboundPads(t *testing.T) {
	st := pads(map[int]bool{2: false, 5: false, 9: false})
	pad, on, done := sweepStep(st, 1)
	if done || !on || pad != 2 {
		t.Fatalf("got pad=%d on=%v done=%v, want pad=2 on", pad, on, done)
	}
}

// The knob targets a count, not a particular set: a pad switched on out of
// order elsewhere counts towards the target instead of being shuffled into pad
// order, so the knob does not fight whoever turned it on.
func TestSweepStepCountsLightsWhereverTheyAre(t *testing.T) {
	st := pads(map[int]bool{1: false, 2: false, 3: true, 4: false})
	if _, _, done := sweepStep(st, 1); !done {
		t.Fatalf("one light lit and one wanted: expected no step")
	}
	pad, on, done := sweepStep(st, 2)
	if done || !on || pad != 1 {
		t.Fatalf("got pad=%d on=%v done=%v, want pad=1 on", pad, on, done)
	}
}
