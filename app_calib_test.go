package main

import "testing"

func TestSaveCCCalibrationRejectsBadRanges(t *testing.T) {
	a := &App{}
	for _, c := range []struct{ lo, hi int }{{-1, 100}, {0, 128}, {50, 50}, {90, 20}} {
		if err := a.SaveCCCalibration(c.lo, c.hi); err == nil {
			t.Fatalf("range %d-%d was accepted; want rejection", c.lo, c.hi)
		}
	}
}
