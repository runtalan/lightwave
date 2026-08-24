package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Calibrated endpoints must survive a save/load cycle, and a legacy config
// with no calibration keys must default to the full 0-127 span.
func TestCalibrationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	s := DefaultSettings()
	s.MidiCCMin = 12
	s.MidiCCMax = 117
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadSettings()
	if got.MidiCCMin != 12 || got.MidiCCMax != 117 {
		t.Fatalf("round trip = %d-%d, want 12-117", got.MidiCCMin, got.MidiCCMax)
	}
	if m := got.MIDI(); m.CCMin != 12 || m.CCMax != 117 {
		t.Fatalf("MIDI() = %d-%d, want 12-117", m.CCMin, m.CCMax)
	}
}

// A config written before calibration existed must not collapse to 0-0.
func TestLegacyConfigDefaultsToFullRange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	p := filepath.Join(dir, "Library", "Application Support", AppName)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"midiCC":7,"midiCCAlt":1,"midiNotePlus":60,"midiNoteMinus":61,"idleHideSeconds":3}`
	if err := os.WriteFile(filepath.Join(p, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadSettings()
	if m := got.MIDI(); m.CCMin != 0 || m.CCMax != 127 {
		t.Fatalf("legacy config = %d-%d, want 0-127", m.CCMin, m.CCMax)
	}
}

// An inverted range must fall back rather than making the fader meaningless.
func TestInvertedCalibrationNormalized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s := DefaultSettings()
	s.MidiCCMin = 100
	s.MidiCCMax = 20
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadSettings()
	if got.MidiCCMin != 0 || got.MidiCCMax != 127 {
		t.Fatalf("inverted kept as %d-%d, want 0-127", got.MidiCCMin, got.MidiCCMax)
	}
}
