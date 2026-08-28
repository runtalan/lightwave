package main

import (
	"testing"

	"lightwave/internal/config"
)

// Escape must throw away in-memory pad edits rather than silently keeping a
// binding the user chose not to save.
func TestDiscardConfigDropsUncommittedPadEdits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A saved map with pad 1 bound.
	saved := config.SlotFile{Configured: true, Slots: make([]config.SlotBinding, 9)}
	for i := range saved.Slots {
		saved.Slots[i] = config.SlotBinding{Slot: i + 1}
	}
	saved.Slots[0] = config.SlotBinding{Slot: 1, DeviceID: "KEEP", Name: "Saved"}
	if err := config.SaveSlotFile(saved); err != nil {
		t.Fatal(err)
	}

	a := &App{configured: true, setupOpen: true, pool: map[int]bool{}}
	a.slots = append([]config.SlotBinding(nil), saved.Slots...)
	// Simulate an unsaved edit: bind something else to pad 2.
	a.slots[1] = config.SlotBinding{Slot: 2, DeviceID: "UNSAVED"}
	a.mapDirty = true
	a.pool[2] = true

	_ = a.DiscardConfig() // CloseConfig may fail with no window; state is what matters

	if a.slots[1].DeviceID != "" {
		t.Fatalf("pad 2 kept discarded binding %q", a.slots[1].DeviceID)
	}
	if a.slots[0].DeviceID != "KEEP" {
		t.Fatalf("saved pad 1 lost: %q", a.slots[0].DeviceID)
	}
	if a.mapDirty {
		t.Fatal("mapDirty still set after discard")
	}
	if a.pool[2] {
		t.Fatal("pad 2 still lit after its binding was discarded")
	}
}
