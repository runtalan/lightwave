package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
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

// PlugCandidate is a plug discovery found, plus whether it is already bound.
type PlugCandidate struct {
	IP    string `json:"ip"`
	MAC   string `json:"mac"`
	Model string `json:"model"`
	Name  string `json:"name"`
	// Supported is false for a plug whose firmware speaks the older AES
	// scheme instead of KLAP. It is listed rather than hidden so the reason a
	// plug cannot be added is visible.
	Supported bool   `json:"supported"`
	Encrypt   string `json:"encrypt"`
	Pad       int    `json:"pad"` // 0 when unbound
}

// ScanPlugs sweeps the LAN for Tapo plugs. It does not need credentials:
// discovery is unauthenticated, so a plug can be found and its protocol
// checked before an account is entered.
func (a *App) ScanPlugs() ([]PlugCandidate, error) {
	found, err := tapo.Discover(3 * time.Second)
	if err != nil {
		return nil, err
	}
	bound := map[string]int{}
	for _, b := range a.plugs.bindings() {
		bound[strings.ToUpper(b.DeviceID)] = b.Pad
	}
	out := make([]PlugCandidate, 0, len(found))
	for _, f := range found {
		out = append(out, PlugCandidate{
			IP:        f.IP,
			MAC:       f.MAC,
			Model:     f.Model,
			Name:      f.Name,
			Supported: f.SupportsKLAP(),
			Encrypt:   f.EncryptType,
			Pad:       bound[strings.ToUpper(f.MAC)],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out, nil
}

// nextPlugPad returns the lowest free pad at or above FirstPlugPad.
func (a *App) nextPlugPad() int {
	taken := map[int]bool{}
	for _, b := range a.plugs.bindings() {
		taken[b.Pad] = true
	}
	for pad := config.FirstPlugPad; ; pad++ {
		if !taken[pad] {
			return pad
		}
	}
}

// AddPlug binds a discovered plug to the next free pad. Plug edits persist
// straight away rather than joining the pad map's dirty/commit cycle: they are
// independent of the numpad, and a plug list is not something you build up in
// memory and commit as a set.
func (a *App) AddPlug(mac, ip, name, model string) (HUDState, error) {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	ip = strings.TrimSpace(ip)
	if mac == "" || ip == "" {
		return a.snapshot(), errors.New("a plug needs both an address and an id")
	}
	binds := a.plugs.bindings()
	for _, b := range binds {
		if strings.EqualFold(b.DeviceID, mac) {
			return a.snapshot(), fmt.Errorf("already on pad %d", b.Pad)
		}
	}
	binds = append(binds, config.PlugBinding{
		Pad:      a.nextPlugPad(),
		DeviceID: mac,
		Name:     strings.TrimSpace(name),
		Model:    strings.TrimSpace(model),
		IP:       ip,
	})
	return a.savePlugs(binds)
}

// RemovePlug unbinds a pad.
func (a *App) RemovePlug(pad int) (HUDState, error) {
	binds := a.plugs.bindings()
	out := make([]config.PlugBinding, 0, len(binds))
	for _, b := range binds {
		if b.Pad != pad {
			out = append(out, b)
		}
	}
	if len(out) == len(binds) {
		return a.snapshot(), errPlugUnbound
	}
	return a.savePlugs(out)
}

// RenamePlug sets a custom label. An empty name clears it, so the discovered
// name shows again.
func (a *App) RenamePlug(pad int, name string) (HUDState, error) {
	binds := a.plugs.bindings()
	found := false
	for i := range binds {
		if binds[i].Pad == pad {
			binds[i].Custom = strings.TrimSpace(name)
			found = true
			break
		}
	}
	if !found {
		return a.snapshot(), errPlugUnbound
	}
	return a.savePlugs(binds)
}

// savePlugs writes the list through the slot file and refreshes the manager.
// The light half of the file is read back rather than assumed, so a plug edit
// can never rewrite the pad map.
func (a *App) savePlugs(binds []config.PlugBinding) (HUDState, error) {
	f := config.LoadSlotFile()
	f.Plugs = config.NormalizePlugs(binds)
	if err := config.SaveSlotFile(f); err != nil {
		return a.snapshot(), err
	}
	a.plugs.setBindings(f.Plugs)
	a.emitState()
	return a.snapshot(), nil
}

// ReloadPlugAccount re-reads the stored credentials. Called after the Plugs
// tab saves, so a freshly entered account takes effect without a restart.
func (a *App) ReloadPlugAccount() HUDState {
	a.loadPlugsFromConfig()
	a.emitState()
	return a.snapshot()
}

// AddPlugByIP binds a plug the broadcast sweep cannot reach — the usual case
// being a plug on another VLAN, where discovery's UDP broadcast never crosses
// the segment but ordinary routed TCP to port 80 does.
//
// It handshakes and reads the plug's own name and model, so a bound plug says
// what it is rather than showing a bare address. Failures are separated by
// stage: which layer broke decides what the user has to fix, and "it didn't
// work" would leave them guessing between a firewall, a password and firmware.
func (a *App) AddPlugByIP(ip string) (HUDState, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return a.snapshot(), errors.New("enter the plug's IP address")
	}
	if net.ParseIP(ip) == nil {
		return a.snapshot(), fmt.Errorf("%q is not an IP address", ip)
	}
	email, password := config.TapoCredentials()
	if email == "" || password == "" {
		return a.snapshot(), errNoTapoAccount
	}
	for _, b := range a.plugs.bindings() {
		if b.IP == ip {
			return a.snapshot(), fmt.Errorf("already on pad %d", b.Pad)
		}
	}

	dev := tapo.New(ip, tapo.Credentials{Username: email, Password: password})
	defer dev.Close()
	status, err := dev.Status()
	if err != nil {
		return a.snapshot(), describePlugFailure(ip, err)
	}

	// No MAC without discovery, so the address is the identity. That is what
	// the KLAP session addresses anyway; a DHCP move needs a re-add either way.
	return a.AddPlug("IP:"+ip, ip, status.Name, status.Model)
}

// describePlugFailure turns a transport error into the thing to go and fix.
func describePlugFailure(ip string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "rejected the credentials"):
		return fmt.Errorf("%s answered, but rejected the account — check the email and password on the Account tab", ip)
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"),
		strings.Contains(msg, "no route"), strings.Contains(msg, "unreachable"):
		return fmt.Errorf("%s did not answer — if it is on another VLAN, allow this Mac to reach it on TCP port 80", ip)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("%s refused the connection — reachable, but nothing is serving the Tapo API on port 80", ip)
	case strings.Contains(msg, "short reply"), strings.Contains(msg, "status 4"), strings.Contains(msg, "status 5"):
		return fmt.Errorf("%s answered but not with KLAP — it may be older AES firmware, which Lightwave cannot drive yet", ip)
	}
	return fmt.Errorf("%s: %w", ip, err)
}
