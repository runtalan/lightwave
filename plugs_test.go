package main

import (
	"testing"

	"lightwave/internal/config"
)

// The whole design rests on plug pads never entering a light path. Both of
// those paths clamp their iteration at nine; this proves a plug pad cannot
// reach them even when it is bound and on.
func TestPlugPadsNeverReachLightPaths(t *testing.T) {
	a := &App{plugs: newPlugManager()}
	a.slots = make([]config.SlotBinding, 9)
	for i := range a.slots {
		a.slots[i] = config.SlotBinding{Slot: i + 1, DeviceID: "LAMP", IP: "192.0.2.1"}
	}
	a.pool = map[int]bool{}
	a.plugs.setCredentials("user@example.com", "pw")
	a.plugs.setBindings([]config.PlugBinding{
		{Pad: 10, DeviceID: "AA:BB", Name: "Fan", IP: "192.0.2.50"},
		{Pad: 11, DeviceID: "CC:DD", Name: "Heater", IP: "192.0.2.51"},
	})

	// Light the whole numpad, and pretend a plug slipped into the pool too —
	// the worst case a future bug could produce.
	for n := 1; n <= 9; n++ {
		a.pool[n] = true
	}
	a.pool[10] = true
	a.pool[11] = true

	a.mu.Lock()
	dests := a.poolDestsLocked()
	ips := a.poolIPsLocked()
	a.mu.Unlock()

	if len(dests) != 9 {
		t.Errorf("poolDestsLocked returned %d destinations, want 9", len(dests))
	}
	if len(ips) != 9 {
		t.Errorf("poolIPsLocked returned %d addresses, want 9", len(ips))
	}
	for _, d := range dests {
		if d.IP == "192.0.2.50" || d.IP == "192.0.2.51" {
			t.Fatalf("a plug reached a light path: %s", d.IP)
		}
	}
}

// ToggleSlot is the single ignite path. Pads at or above FirstPlugPad must
// route to the plug manager rather than being rejected as out of range.
func TestToggleSlotRoutesPlugPads(t *testing.T) {
	a := &App{plugs: newPlugManager()}
	a.slots = make([]config.SlotBinding, 9)
	a.pool = map[int]bool{}
	a.plugs.setBindings([]config.PlugBinding{{Pad: 10, DeviceID: "AA", IP: "192.0.2.50"}})

	// Bound plug, no account: the error must name the account, not the range.
	err := a.ToggleSlot(10)
	if err == nil {
		t.Fatal("expected an error with no plug bound")
	}
	if err.Error() == "slot must be 1-9" {
		t.Fatalf("plug pad was rejected as a light pad: %v", err)
	}

	if err != errNoTapoAccount {
		t.Errorf("bound plug with no account: got %v", err)
	}

	// An unbound high pad is out of range, not an account problem.
	if err := a.ToggleSlot(99); err == nil || err.Error() != "slot must be 1-9" {
		t.Errorf("pad 99 is neither a light nor a bound plug: %v", err)
	}
}

// A plug's absence of an account must be distinguishable from an unbound pad,
// so the UI can tell the user which one to fix.
func TestPlugErrorsAreDistinct(t *testing.T) {
	m := newPlugManager()
	m.setBindings([]config.PlugBinding{{Pad: 10, DeviceID: "AA", IP: "192.0.2.50"}})

	if err := m.setOn(10, true); err != errNoTapoAccount {
		t.Errorf("bound plug with no account: got %v, want errNoTapoAccount", err)
	}

	m.setCredentials("u@example.com", "pw")
	if err := m.setOn(11, true); err != errPlugUnbound {
		t.Errorf("unbound pad with an account: got %v, want errPlugUnbound", err)
	}
}

// Changing the account must drop live sessions: they were negotiated against
// the old credentials and every later request would 403.
func TestCredentialChangeDropsSessions(t *testing.T) {
	m := newPlugManager()
	m.setCredentials("first@example.com", "pw1")
	m.setBindings([]config.PlugBinding{{Pad: 10, DeviceID: "AA", IP: "192.0.2.50"}})

	m.mu.Lock()
	_, _ = m.deviceForLocked(10)
	had := m.live[10] != nil && m.live[10].dev != nil
	m.mu.Unlock()
	if !had {
		t.Fatal("expected a device handle after first use")
	}

	m.setCredentials("second@example.com", "pw2")
	m.mu.Lock()
	still := len(m.live)
	m.mu.Unlock()
	if still != 0 {
		t.Errorf("live sessions survived a credential change: %d", still)
	}
}

// Unbinding a plug must release its session rather than leaking it.
func TestRebindingDropsStaleSessions(t *testing.T) {
	m := newPlugManager()
	m.setCredentials("u@example.com", "pw")
	m.setBindings([]config.PlugBinding{
		{Pad: 10, DeviceID: "AA", IP: "192.0.2.50"},
		{Pad: 11, DeviceID: "BB", IP: "192.0.2.51"},
	})
	m.mu.Lock()
	_, _ = m.deviceForLocked(10)
	_, _ = m.deviceForLocked(11)
	m.mu.Unlock()

	// Drop pad 11.
	m.setBindings([]config.PlugBinding{{Pad: 10, DeviceID: "AA", IP: "192.0.2.50"}})
	m.mu.Lock()
	_, gone := m.live[11]
	m.mu.Unlock()
	if gone {
		t.Error("session for an unbound pad was not released")
	}
}

// normalizePlugs is the guard that stops a malformed file from putting a plug
// on a light's pad.
func TestNormalizePlugsRejectsLightPads(t *testing.T) {
	f := config.SlotFile{
		Configured: true,
		Slots:      make([]config.SlotBinding, 9),
		Plugs: []config.PlugBinding{
			{Pad: 3, DeviceID: "AA", IP: "192.0.2.50"},  // a light's pad
			{Pad: 10, DeviceID: "BB", IP: "192.0.2.51"}, // fine
			{Pad: 11, DeviceID: "", IP: "192.0.2.52"},   // unbound
			{Pad: 10, DeviceID: "CC", IP: "192.0.2.53"}, // duplicate pad
		},
	}
	// SaveSlotFile normalises on the way out; exercise the same path via a
	// temp HOME so nothing touches the real config.
	t.Setenv("HOME", t.TempDir())

	got := config.NormalizePlugs(f.Plugs)
	if len(got) != 1 {
		t.Fatalf("expected exactly one surviving plug, got %d: %+v", len(got), got)
	}
	if got[0].Pad != 10 || got[0].DeviceID != "BB" {
		t.Errorf("wrong plug survived: %+v", got[0])
	}
}

// Plugs must be visible to the snapshot without the light slots changing.
func TestSnapshotCarriesPlugs(t *testing.T) {
	a := &App{plugs: newPlugManager()}
	a.slots = make([]config.SlotBinding, 9)
	a.pool = map[int]bool{}
	a.plugs.setBindings([]config.PlugBinding{
		{Pad: 10, DeviceID: "AA", Name: "Fan", IP: "192.0.2.50"},
	})

	views := a.GetPlugs()
	if len(views) != 1 {
		t.Fatalf("expected 1 plug view, got %d", len(views))
	}
	if views[0].Pad != 10 || views[0].Name != "Fan" {
		t.Errorf("unexpected view: %+v", views[0])
	}
	if !views[0].Bound {
		t.Error("a plug with a device id should read as bound")
	}
}

// Pads are handed out from FirstPlugPad upward, filling gaps left by removals
// rather than climbing forever.
func TestNextPlugPadFillsGaps(t *testing.T) {
	a := &App{plugs: newPlugManager()}
	if got := a.nextPlugPad(); got != config.FirstPlugPad {
		t.Errorf("first plug should take pad %d, got %d", config.FirstPlugPad, got)
	}

	a.plugs.setBindings([]config.PlugBinding{
		{Pad: 10, DeviceID: "AA", IP: "192.0.2.50"},
		{Pad: 12, DeviceID: "CC", IP: "192.0.2.52"},
	})
	if got := a.nextPlugPad(); got != 11 {
		t.Errorf("expected the gap at 11, got %d", got)
	}

	a.plugs.setBindings([]config.PlugBinding{
		{Pad: 10, DeviceID: "AA", IP: "192.0.2.50"},
		{Pad: 11, DeviceID: "BB", IP: "192.0.2.51"},
	})
	if got := a.nextPlugPad(); got != 12 {
		t.Errorf("expected 12 after a contiguous run, got %d", got)
	}
}

// Saving plugs must never rewrite the pad map: the two live in one file, and
// a plug edit reads the light half back rather than assuming it.
func TestSavePlugsPreservesLights(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	lights := make([]config.SlotBinding, 9)
	for i := range lights {
		lights[i] = config.SlotBinding{Slot: i + 1, DeviceID: "LAMP" + string(rune('A'+i)), IP: "192.0.2.1"}
	}
	if err := config.SaveSlotFile(config.SlotFile{Configured: true, Slots: lights}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	a := &App{plugs: newPlugManager()}
	a.slots = lights
	a.pool = map[int]bool{}
	if _, err := a.savePlugs([]config.PlugBinding{
		{Pad: 10, DeviceID: "AA", Name: "Fan", IP: "192.0.2.50"},
	}); err != nil {
		t.Fatalf("savePlugs: %v", err)
	}

	got := config.LoadSlotFile()
	if len(got.Plugs) != 1 || got.Plugs[0].Pad != 10 {
		t.Fatalf("plug not saved: %+v", got.Plugs)
	}
	for i, s := range got.Slots {
		if s.DeviceID == "" {
			t.Fatalf("a plug edit cleared light slot %d", i+1)
		}
	}
}
