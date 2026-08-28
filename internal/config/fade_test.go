package config

import (
	"testing"
	"time"
)

func TestFadeDuration(t *testing.T) {
	cases := []struct {
		name string
		set  int
		want time.Duration
	}{
		// A config.json written before this setting existed has no value at
		// all, which must read as the default rather than an instant tour.
		{"unset", 0, DefaultFadeSeconds * time.Second},
		{"normal", 30, 30 * time.Second},
		{"at min", MinFadeSeconds, MinFadeSeconds * time.Second},
		{"at max", MaxFadeSeconds, MaxFadeSeconds * time.Second},
		// Out-of-range values are clamped, not honoured: a 1s tour would
		// strobe every pooled lamp and flood the network.
		{"too fast", 1, MinFadeSeconds * time.Second},
		{"negative", -10, DefaultFadeSeconds * time.Second},
		{"too slow", 9000, MaxFadeSeconds * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Settings{FadeSeconds: c.set}.FadeDuration()
			if got != c.want {
				t.Fatalf("FadeSeconds %d = %v, want %v", c.set, got, c.want)
			}
		})
	}
}

// The setting has to survive a save/load round trip, or changing it in the
// config window would revert on the next launch.
func TestFadeSecondsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := DefaultSettings()
	if s.FadeSeconds != DefaultFadeSeconds {
		t.Fatalf("default FadeSeconds = %d, want %d", s.FadeSeconds, DefaultFadeSeconds)
	}
	s.FadeSeconds = 20
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadSettings().FadeSeconds; got != 20 {
		t.Fatalf("loaded FadeSeconds = %d, want 20", got)
	}
}

func TestDriftAmount(t *testing.T) {
	cases := []struct {
		name string
		s    Settings
		want float64
	}{
		{"default", Settings{FadeDrift: DefaultFadeDrift, FadeSeconds: 60}, 1},
		{"narrowed", Settings{FadeDrift: 50, FadeSeconds: 60}, 0.5},
		{"widened", Settings{FadeDrift: 200, FadeSeconds: 60}, 2},
		// 0 is a real choice when the user set it: hold on one colour.
		{"deliberate zero", Settings{FadeDrift: 0, FadeSeconds: 30}, 0},
		{"clamped high", Settings{FadeDrift: 9000, FadeSeconds: 60}, float64(MaxFadeDrift) / 100},
		{"clamped low", Settings{FadeDrift: -50, FadeSeconds: 60}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.s.DriftAmount(); got != c.want {
				t.Fatalf("DriftAmount = %v, want %v", got, c.want)
			}
		})
	}
}

// A config.json written before either fade setting existed has both fields
// absent, which decodes as 0/0. That must read as "unchanged", not as a
// deliberate zero drift — otherwise upgrading would freeze everyone's fade on
// a single colour.
func TestDriftAmountUpgradeFromOldConfig(t *testing.T) {
	if got := (Settings{}).DriftAmount(); got != 1 {
		t.Fatalf("untouched settings DriftAmount = %v, want 1", got)
	}
}

func TestFadeDriftRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := DefaultSettings()
	if s.FadeDrift != DefaultFadeDrift {
		t.Fatalf("default FadeDrift = %d, want %d", s.FadeDrift, DefaultFadeDrift)
	}
	s.FadeDrift = 180
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadSettings().FadeDrift; got != 180 {
		t.Fatalf("loaded FadeDrift = %d, want 180", got)
	}
}
