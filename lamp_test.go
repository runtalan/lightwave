package main

import "testing"

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
