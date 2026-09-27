package config

import "testing"

// Stopping the MIDI listener has to survive a relaunch, and a settings file
// written before the toggle existed must still start listening.
func TestMidiDisabledRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if LoadSettings().MidiDisabled {
		t.Fatal("fresh settings start with MIDI disabled")
	}
	s := DefaultSettings()
	s.MidiDisabled = true
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !LoadSettings().MidiDisabled {
		t.Fatal("MidiDisabled did not survive a save/load round trip")
	}
}
