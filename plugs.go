package main

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"lightwave/internal/config"
	"lightwave/internal/tapo"
)

// Plugs are deliberately not lights. They live in their own list on pads 10+,
// outside a.slots, because every light path clamps its iteration at nine
// (poolDestsLocked, AllOn). That clamp is what keeps brightness writes, the
// colour fade and gradient painting from ever reaching a plug, which has no
// brightness and no colour. Keeping the lists separate means none of those
// paths needed a plug check added to them.
const (
	// Plug status is a TCP round trip with a session behind it, unlike the
	// lights' fire-and-forget UDP, so it polls far slower than statusPollLoop.
	// A plug is binary; nobody perceives lag on an on/off dot the way they do
	// on a dimmer.
	plugPollActive = 5 * time.Second
	plugPollIdle   = 60 * time.Second
)

// Errors a caller can act on: one means "set up an account", the other means
// "this pad has no plug". Both are user-fixable, so they are distinguished
// rather than collapsed into a generic failure.
var (
	errNoTapoAccount = errors.New("no TP-Link account set — add one in Config → Plugs")
	errPlugUnbound   = errors.New("no plug bound to this pad")
)

// PlugView is one plug as the UI sees it.
type PlugView struct {
	Pad    int    `json:"pad"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	IP     string `json:"ip"`
	Bound  bool   `json:"bound"`
	On     bool   `json:"on"`
	Online bool   `json:"online"`
}

// plugState is the runtime half of a binding: the live KLAP session plus what
// the plug last reported.
type plugState struct {
	dev    *tapo.Device
	on     bool
	online bool
	// touched suppresses a poll reply that may predate a command we just
	// issued, the same guard applyDeviceStatus uses for lamps.
	touched time.Time
}

type plugManager struct {
	mu    sync.Mutex
	binds []config.PlugBinding
	live  map[int]*plugState // by pad
	creds tapo.Credentials
}

func newPlugManager() *plugManager {
	return &plugManager{live: map[int]*plugState{}}
}

// setCredentials swaps the account. Existing sessions are dropped: they were
// negotiated against the old one and every later request would 403.
func (m *plugManager) setCredentials(email, password string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := tapo.Credentials{Username: email, Password: password}
	if next == m.creds {
		return
	}
	m.creds = next
	for _, st := range m.live {
		if st.dev != nil {
			st.dev.Close()
		}
	}
	m.live = map[int]*plugState{}
}

func (m *plugManager) setBindings(binds []config.PlugBinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.binds = append([]config.PlugBinding(nil), binds...)
	keep := map[int]bool{}
	for _, b := range binds {
		keep[b.Pad] = true
	}
	for pad, st := range m.live {
		if !keep[pad] {
			if st.dev != nil {
				st.dev.Close()
			}
			delete(m.live, pad)
		}
	}
}

func (m *plugManager) bindings() []config.PlugBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]config.PlugBinding(nil), m.binds...)
}

// configured reports whether an account is set. Without one there is nothing
// to hand the handshake, so callers should say so rather than fail per plug.
func (m *plugManager) configured() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.creds.Username != "" && m.creds.Password != ""
}

// deviceFor returns the KLAP handle for a pad, creating it on first use.
// Caller must hold m.mu.
func (m *plugManager) deviceForLocked(pad int) (*tapo.Device, bool) {
	st := m.live[pad]
	if st != nil && st.dev != nil {
		return st.dev, true
	}
	var bind config.PlugBinding
	found := false
	for _, b := range m.binds {
		if b.Pad == pad {
			bind, found = b, true
			break
		}
	}
	if !found || strings.TrimSpace(bind.IP) == "" {
		return nil, false
	}
	if m.creds.Username == "" || m.creds.Password == "" {
		return nil, false
	}
	dev := tapo.New(bind.IP, m.creds)
	if st == nil {
		st = &plugState{}
		m.live[pad] = st
	}
	st.dev = dev
	return dev, true
}

// views renders the plug list for a snapshot.
func (m *plugManager) views() []PlugView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PlugView, 0, len(m.binds))
	for _, b := range m.binds {
		st := m.live[b.Pad]
		v := PlugView{
			Pad:   b.Pad,
			Name:  b.Label(),
			Model: b.Model,
			IP:    b.IP,
			Bound: b.DeviceID != "",
		}
		if st != nil {
			v.On, v.Online = st.on, st.online
		}
		out = append(out, v)
	}
	return out
}

// isPlugPad reports whether this pad number belongs to a bound plug.
func (m *plugManager) isPlugPad(pad int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.binds {
		if b.Pad == pad {
			return true
		}
	}
	return false
}

// setOn drives one plug and records the new state optimistically, so the HUD
// and the Stream Deck respond to the press rather than to the next poll.
func (m *plugManager) setOn(pad int, on bool) error {
	m.mu.Lock()
	dev, ok := m.deviceForLocked(pad)
	if !ok {
		m.mu.Unlock()
		if !m.configured() {
			return errNoTapoAccount
		}
		return errPlugUnbound
	}
	st := m.live[pad]
	m.mu.Unlock()

	if err := dev.SetOn(on); err != nil {
		m.mu.Lock()
		if st != nil {
			st.online = false
		}
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	if st != nil {
		st.on, st.online, st.touched = on, true, time.Now()
	}
	m.mu.Unlock()
	return nil
}

func (m *plugManager) isOn(pad int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.live[pad]
	return st != nil && st.on
}

// poll refreshes every plug. Returns true when anything changed, so the caller
// only re-emits state when there is news.
func (m *plugManager) poll() bool {
	m.mu.Lock()
	pads := make([]int, 0, len(m.binds))
	for _, b := range m.binds {
		pads = append(pads, b.Pad)
	}
	m.mu.Unlock()

	changed := false
	for _, pad := range pads {
		m.mu.Lock()
		dev, ok := m.deviceForLocked(pad)
		st := m.live[pad]
		recent := st != nil && time.Since(st.touched) < commandSettle
		m.mu.Unlock()
		if !ok || recent {
			continue
		}

		status, err := dev.Status()
		m.mu.Lock()
		if st != nil {
			if err != nil {
				if st.online {
					changed = true
				}
				st.online = false
			} else {
				if st.on != status.On || !st.online {
					changed = true
				}
				st.on, st.online = status.On, true
			}
		}
		m.mu.Unlock()
	}
	return changed
}

// close drops every session.
func (m *plugManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.live {
		if st.dev != nil {
			st.dev.Close()
		}
	}
	m.live = map[int]*plugState{}
}

// plugPollLoop is one goroutine for every plug, not one each: a handful of
// sequential TCP round trips every few seconds is cheaper than the goroutines
// and sockets a fan-out would hold open.
func (a *App) plugPollLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("plug poll recovered: %v", r)
		}
	}()
	t := time.NewTimer(plugPollActive)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}

		a.mu.Lock()
		idle := a.hidden || time.Since(a.lastActivity) > statusPollActiveFor
		dancing := a.dancing
		a.mu.Unlock()

		// The fade writes colours continuously; it never touches plugs, but
		// the traffic is unwelcome while it runs.
		if !dancing && a.plugs.configured() {
			if a.plugs.poll() {
				a.scheduleStateEmit()
			}
		}

		next := plugPollActive
		if idle {
			next = plugPollIdle
		}
		t.Reset(next)
	}
}

// loadPlugsFromConfig seeds the manager from the persisted slot file and the
// stored account. Called at startup and whenever either changes.
func (a *App) loadPlugsFromConfig() {
	if a.plugs == nil {
		return
	}
	email, password := config.TapoCredentials()
	a.plugs.setCredentials(email, password)
	a.plugs.setBindings(config.LoadSlotFile().Plugs)
}

// TogglePlug flips one plug pad. Separate from ToggleSlot on purpose: a plug
// never joins a.pool, so none of the scene bookkeeping — recall, brightness,
// the fade — can pick it up.
func (a *App) TogglePlug(pad int) error {
	if a.plugs == nil {
		return errPlugUnbound
	}
	a.recordUserActivity()
	want := !a.plugs.isOn(pad)
	if err := a.plugs.setOn(pad, want); err != nil {
		return err
	}
	a.scheduleStateEmit()
	return nil
}

// SetPlugOn is the idempotent form, for controllers that send explicit
// on/off rather than a toggle.
func (a *App) SetPlugOn(pad int, on bool) error {
	if a.plugs == nil {
		return errPlugUnbound
	}
	a.recordUserActivity()
	if a.plugs.isOn(pad) == on {
		return nil
	}
	if err := a.plugs.setOn(pad, on); err != nil {
		return err
	}
	a.scheduleStateEmit()
	return nil
}

// GetPlugs is the UI's view of the plug list.
func (a *App) GetPlugs() []PlugView {
	if a.plugs == nil {
		return nil
	}
	return a.plugs.views()
}

// plugViewsLocked is called with a.mu held. It only touches the plug
// manager's own mutex, which never reaches back for a.mu, so the nesting is
// one-directional and cannot deadlock.
func (a *App) plugViewsLocked() []PlugView {
	if a.plugs == nil {
		return nil
	}
	return a.plugs.views()
}
