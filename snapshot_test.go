package main

import (
	"testing"

	"lightwave/internal/config"
	"lightwave/internal/govee"
)

func TestSnapshotOmitsCatalogOnHUD(t *testing.T) {
	a := &App{
		slots:     make([]config.SlotBinding, 9),
		pool:      map[int]bool{},
		catalog:   []govee.Device{{ID: "ABC", Name: "Lamp", Model: "H6001"}},
		setupOpen: false,
	}
	for i := range a.slots {
		a.slots[i].Slot = i + 1
	}

	st := a.snapshot()
	if len(st.Catalog) != 0 {
		t.Fatalf("HUD snapshot leaked catalog: %+v", st.Catalog)
	}
	if st.DeviceCount != 1 {
		t.Fatalf("DeviceCount = %d, want 1 so the HUD can still show a count", st.DeviceCount)
	}

	a.setupOpen = true
	st = a.snapshot()
	if len(st.Catalog) != 1 || st.Catalog[0].ID != "ABC" {
		t.Fatalf("config snapshot missing catalog: %+v", st.Catalog)
	}
}

func TestSnapshotKeepsModelVisibleWithCustomName(t *testing.T) {
	a := &App{
		slots: make([]config.SlotBinding, 9),
		pool:  map[int]bool{},
	}
	for i := range a.slots {
		a.slots[i].Slot = i + 1
	}
	a.slots[0] = config.SlotBinding{
		Slot:     1,
		DeviceID: "ABC",
		Name:     "H6001",
		Custom:   "Desk lamp",
		Model:    "H6001",
		IP:       "ble:ABC",
	}

	got := a.snapshot().Slots[0]
	if got.Name != "Desk lamp" {
		t.Fatalf("Name = %q, want custom name", got.Name)
	}
	if got.Model != "H6001" {
		t.Fatalf("Model = %q, want canonical model preserved", got.Model)
	}
}
