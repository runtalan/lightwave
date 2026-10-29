package config

import "testing"

// MIDI starts stopped by default, and turning it on or off has to survive a
// relaunch.
func TestMidiDisabledRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if !LoadSettings().MidiDisabled {
		t.Fatal("fresh settings should start with MIDI stopped")
	}
	s := DefaultSettings()
	s.MidiDisabled = false
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	if LoadSettings().MidiDisabled {
		t.Fatal("MIDI listening did not survive a save/load round trip")
	}
	s.MidiDisabled = true
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !LoadSettings().MidiDisabled {
		t.Fatal("MidiDisabled did not survive a save/load round trip")
	}
}
