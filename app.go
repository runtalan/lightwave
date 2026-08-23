package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lightwave/internal/color"
	"lightwave/internal/config"
	"lightwave/internal/govee"
	midilstn "lightwave/internal/midi"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Window geometry. The HUD is a compact deck; Config is taller because the
// device list scrolls inside it.
const (
	HUDW       = 520
	HUDH       = 620
	ConfigW    = 680
	ConfigH    = 880
	WindowMinW = 420
	WindowMinH = 480
)

// Govee LAN control is UDP with no flow control, and the devices are small
// embedded controllers. A fast slider drag can generate events far quicker
// than a lamp can act on them, so brightness writes are both coalesced and
// rate limited: at most one write per pool every brightMinInterval.
const (
	brightCoalesce    = 16 * time.Millisecond
	brightMinInterval = 100 * time.Millisecond

	// Status polling is adaptive: responsive while the HUD is in use, then
	// backing off hard once it is hidden or idle. A fixed 1s poll would be
	// ~500k packets/day for three lamps to keep a window nobody is looking at
	// up to date.
	statusPollActive = time.Second
	statusPollIdle   = 30 * time.Second
	// statusPollActiveFor is how long after the last interaction the fast rate
	// persists.
	statusPollActiveFor = 30 * time.Second
	// brightSyncGuard keeps a poll reply from yanking the slider out from
	// under an adjustment the user is still making.
	brightSyncGuard = 1500 * time.Millisecond
	// commandSettle is how long a just-commanded slot ignores devStatus
	// replies. Govee lamps need a moment before they report a new power state.
	commandSettle = 3 * time.Second

	// danceStep is the interval between colour updates during the fade
	// animation, and dancePeriod is how long one full tour of the palette
	// takes. The step is deliberately slower than the brightness rate limit:
	// each tick writes to every pooled lamp, and the effect should drift
	// rather than strobe.
	danceStep   = 900 * time.Millisecond
	dancePeriod = 60 * time.Second
)

// sendPoolBrightness is a seam so tests can observe the LAN write rate.
// Swapped only via setSendPoolBrightness, which is race-safe.
var (
	sendMu       sync.RWMutex
	sendPoolFunc = govee.ApplyPoolBrightness
)

func sendPoolBrightness(ips []string, percent int, turnOn bool) {
	sendMu.RLock()
	f := sendPoolFunc
	sendMu.RUnlock()
	f(ips, percent, turnOn)
}

// sendTurn is a seam so tests can observe on/off commands.
var sendTurn = govee.SendTurn

type SlotView struct {
	Number   int    `json:"number"`
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	IP       string `json:"ip"`
	Active   bool   `json:"active"`
	Online   bool   `json:"online"`
}

type HUDState struct {
	Slots         []SlotView     `json:"slots"`
	ActivePool    []int          `json:"activePool"`
	Brightness    int            `json:"brightness"`
	PaletteIndex  int            `json:"paletteIndex"`
	PaletteName   string         `json:"paletteName"`
	MIDIConnected bool           `json:"midiConnected"`
	MIDIPort      string         `json:"midiPort"`
	DeviceCount   int            `json:"deviceCount"`
	NeedsSetup    bool           `json:"needsSetup"`
	SetupOpen     bool           `json:"setupOpen"`
	HasAPIKey     bool           `json:"hasApiKey"`
	DiscoverError string         `json:"discoverError"`
	Discovering   bool           `json:"discovering"`
	FirstRun      bool           `json:"firstRun"`
	Catalog       []govee.Device `json:"catalog"`
	Hidden        bool           `json:"hidden"`
	MappingPath   string         `json:"mappingPath"`
	ConfigOpen    bool           `json:"configOpen"`
	Dancing       bool           `json:"dancing"`
	Settings      SettingsView   `json:"settings"`
}

type SettingsView struct {
	MidiCC          int    `json:"midiCC"`
	MidiCCAlt       int    `json:"midiCCAlt"`
	MidiNotePlus    int    `json:"midiNotePlus"`
	MidiNoteMinus   int    `json:"midiNoteMinus"`
	IdleHideSeconds int    `json:"idleHideSeconds"`
	HasEnvKey       bool   `json:"hasEnvKey"`
	HasConfigKey    bool   `json:"hasConfigKey"`
	HasAPIKey       bool   `json:"hasApiKey"`
	EnvPath         string `json:"envPath"`
	ConfigPath      string `json:"configPath"`
	MappingPath     string `json:"mappingPath"`
}

type App struct {
	// ctx is written once by startup and read by every background goroutine
	// (MIDI drain, brightness pump, idle loop, IPC). It must be accessed only
	// through ctxOK/setCtx: an unsynchronized read can hand Wails a context
	// that has no "events" value, and Wails answers that with log.Fatalf —
	// an immediate os.Exit that no recover() can stop.
	ctxVal atomic.Pointer[context.Context]

	mu           sync.Mutex
	slots        []config.SlotBinding
	configured   bool
	setupOpen    bool
	forceSetup   bool
	pool         map[int]bool
	brightness   int
	engine       color.Engine
	catalog      []govee.Device
	discoverErr  string
	discovering  bool
	midiOK       bool
	midiPort     string
	hidden       bool
	hiding       bool
	lastActivity time.Time
	shownAt      time.Time
	uiReady      bool
	fadeCancel   int
	sizedMode    int // 0 never sized, 1 HUD, 2 config

	udp      *govee.UDP
	ble      *govee.BLE
	midi     *midilstn.Listener
	midiCfg  config.MIDI
	settings config.Settings

	stop            chan struct{}
	brightKick      chan struct{}
	pendingBright   atomic.Int32
	emitPending     atomic.Uint32
	lastSentBright  int
	dragging        bool
	lastBrightTouch time.Time
	hiddenAt        time.Time
	dancing         bool
	danceGen        int
	// slotTouched[n] is when the user last commanded slot n. A devStatus reply
	// that predates the command must not undo it: Govee lamps take a beat to
	// report a new power state, and believing a stale reply would toggle the
	// pad straight back off.
	slotTouched map[int]time.Time
}

func NewApp(forceSetup bool) *App {
	f := config.LoadSlotFile()
	needs := config.NeedsSetup(f)
	settings := config.LoadSettings()
	a := &App{
		slots:        f.Slots,
		configured:   !needs,
		forceSetup:   forceSetup,
		setupOpen:    forceSetup || needs,
		pool:         map[int]bool{},
		slotTouched:  map[int]time.Time{},
		brightness:   80,
		udp:          govee.NewUDP(),
		ble:          govee.NewBLE(),
		settings:     settings,
		midiCfg:      settings.MIDI(),
		lastActivity: time.Now(),
		stop:         make(chan struct{}),
		brightKick:   make(chan struct{}, 1),
	}
	if len(a.slots) != 9 {
		a.slots = config.LoadSlotFile().Slots
	}
	// main.go already sizes the launch window to match the mode; remember it
	// so the first sizeForMode call doesn't redo the work.
	a.sizedMode = 1
	if a.setupOpen {
		a.sizedMode = 2
	}
	a.pendingBright.Store(80)
	a.lastSentBright = -1
	return a
}

func (a *App) IsSetupOpen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupOpen
}

// setCtx publishes the lifecycle context. Called once, from startup, before
// any goroutine that emits is launched.
func (a *App) setCtx(ctx context.Context) {
	if ctx == nil {
		return
	}
	a.ctxVal.Store(&ctx)
}

// ctxOK returns the lifecycle context and whether it is safe to hand to the
// Wails runtime. It rejects a context that carries no "events" value, which is
// the exact input that makes the runtime call log.Fatalf and kill the process.
func (a *App) ctxOK() (context.Context, bool) {
	p := a.ctxVal.Load()
	if p == nil || *p == nil {
		return nil, false
	}
	ctx := *p
	if ctx.Value("events") == nil {
		return nil, false
	}
	return ctx, true
}

func (a *App) startup(ctx context.Context) {
	a.setCtx(ctx)
	config.LoadEnv()
	a.mu.Lock()
	a.settings = config.LoadSettings()
	a.midiCfg = a.settings.MIDI()
	a.mu.Unlock()

	a.udp.OnStatus(a.applyDeviceStatus)
	if err := a.udp.Start(func(d govee.Device) {
		a.mergeLAN(d)
		a.emitState()
		// A newly discovered device: ask what it is currently doing so the HUD
		// reflects reality rather than assuming everything is off.
		if ip := d.IP; ip != "" {
			go func() { _ = govee.QueryStatus(ip) }()
		}
	}); err != nil {
		log.Printf("govee lan: %v", err)
	}

	a.ble.StartTransport()
	if err := a.ble.Start(func(d govee.Device) {
		a.mergeBLE(d)
		a.emitState()
	}); err != nil {
		log.Printf("govee ble: %v", err)
	}

	a.midi = midilstn.New(a.midiCfg)
	go a.midiApplyLoop()
	go a.brightnessPump()
	go func() {
		_ = a.midi.Start()
	}()

	runtime.WindowShow(ctx)
	runtime.WindowCenter(ctx)
	a.noteShow()

	// Clicking the Dock icon while the HUD is hidden must bring it back.
	// AppKit's reopen event has no Wails hook, so watch for its signature
	// instead: the app becoming frontmost with no visible window.
	go a.dockReopenLoop()

	go a.refreshDevices()
	go a.inactivityLoop()
	go a.statusPollLoop()
	go func() {
		t := time.NewTicker(45 * time.Second)
		defer t.Stop()
		n := 0
		for range t.C {
			_ = a.udp.Scan()
			// BLE discovery holds the radio for a whole window, so rescan on
			// a slower beat than the cheap UDP broadcast.
			if n++; n%4 == 0 {
				a.ble.Scan()
			}
			a.foldBLE()
		}
	}()
	// The first BLE finds can land before the cloud catalog does; one early
	// re-fold links them to their cloud names without waiting for the ticker.
	time.AfterFunc(8*time.Second, a.foldBLE)
}

func (a *App) shutdown(ctx context.Context) {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
	if a.midi != nil {
		a.midi.Close()
	}
	if a.ble != nil {
		a.ble.Close()
	}
	if a.udp != nil {
		a.udp.Close()
	}
}

func (a *App) midiApplyLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("midi: apply loop recovered: %v", r)
		}
	}()
	t := time.NewTicker(8 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.drainMIDI()
		}
	}
}

func (a *App) drainMIDI() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("midi: drain recovered: %v", r)
		}
	}()
	if a.midi == nil {
		return
	}
	if ok, port, changed := a.midi.TakeStatus(); changed {
		a.mu.Lock()
		a.midiOK = ok
		a.midiPort = port
		a.mu.Unlock()
		a.emitState()
	}
	if _, val, ok := a.midi.TakeCC(); ok {
		a.applyBrightness(midilstn.CCToPercent(val), false)
	}
	if note, ok := a.midi.TakeNote(); ok {
		a.mu.Lock()
		plus, minus := a.midiCfg.NotePlus, a.midiCfg.NoteMinus
		a.mu.Unlock()
		if note == plus {
			a.CycleColor(1)
		} else if note == minus {
			a.CycleColor(-1)
		}
	}
}

func (a *App) scheduleStateEmit() {
	if !a.emitPending.CompareAndSwap(0, 1) {
		return
	}
	go func() {
		defer func() {
			a.emitPending.Store(0)
			if r := recover(); r != nil {
				log.Printf("midi: emit recovered: %v", r)
			}
		}()
		time.Sleep(32 * time.Millisecond)
		a.emitState()
	}()
}

func (a *App) HandleIPC(cmd string) {
	switch cmd {
	case "SETUP", "CONFIG":
		a.OpenConfig()
		a.ShowHUD()
	case "SHOW":
		a.ShowHUD()
	default:
		a.ToggleWindow()
	}
}

func (a *App) mergeLAN(d govee.Device) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := govee.NormalizeID(d.ID)
	found := false
	for i, c := range a.catalog {
		if govee.NormalizeID(c.ID) == id {
			c.IP = d.IP
			c.Online = true
			if c.Model == "" {
				c.Model = d.Model
			}
			a.catalog[i] = c
			found = true
			break
		}
	}
	if !found {
		a.catalog = append(a.catalog, d)
	}
	for i, s := range a.slots {
		if govee.NormalizeID(s.DeviceID) == id {
			a.slots[i].IP = d.IP
			// Treat a name that is just the model code as no name at all:
			// older builds persisted the SKU here, and the friendly name from
			// cloud discovery should replace it.
			if a.slots[i].Name == "" || a.slots[i].Name == a.slots[i].Model {
				if d.Name != "" {
					a.slots[i].Name = d.Name
				}
			}
			if a.slots[i].Model == "" {
				a.slots[i].Model = d.Model
			}
		}
	}
}

// mergeBLE folds a Bluetooth discovery into the catalog and slots. A BLE
// address is a fallback link: it never replaces a working LAN IP. When the
// peripheral can be matched to a cloud identity — same model, and the MAC
// tail from its advertised name appearing inside the cloud device ID — it
// adopts that entry (and its friendly name) instead of duplicating it.
func (a *App) mergeBLE(d govee.Device) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mergeBLELocked(d)
}

func (a *App) mergeBLELocked(d govee.Device) {
	suffix := govee.BLESuffixFromName(d.AdvName)
	target := -1
	if suffix != "" && d.Model != "" {
		for i, c := range a.catalog {
			if strings.EqualFold(c.Model, d.Model) && strings.Contains(govee.NormalizeID(c.ID), suffix) {
				target = i
				break
			}
		}
	}
	if target == -1 {
		id := govee.NormalizeID(d.ID)
		for i, c := range a.catalog {
			if govee.NormalizeID(c.ID) == id {
				target = i
				break
			}
		}
	}
	var id, best string
	if target >= 0 {
		c := a.catalog[target]
		if a.bleMayReplaceLocked(c.IP) {
			c.IP = d.IP
		}
		if c.Model == "" {
			c.Model = d.Model
		}
		c.Online = true
		a.catalog[target] = c
		id = govee.NormalizeID(c.ID)
		best = c.Name
	} else {
		if d.Name == "" {
			d.Name = govee.BLEFallbackName(d.Model, suffix)
		}
		a.catalog = append(a.catalog, d)
		id = govee.NormalizeID(d.ID)
		best = d.Name
	}
	for i, s := range a.slots {
		if govee.NormalizeID(s.DeviceID) != id {
			continue
		}
		if a.bleMayReplaceLocked(s.IP) {
			if a.slots[i].IP != d.IP {
				log.Printf("govee ble: pad %d (%s) now reachable over bluetooth", i+1, s.Name)
			}
			a.slots[i].IP = d.IP
		}
		if a.slots[i].Model == "" {
			a.slots[i].Model = d.Model
		}
		// Upgrade placeholder names ("H617A", "H617A (BLE)") to the best we
		// have — the cloud name for matched lamps, the suffixed fallback
		// otherwise — but never touch a name the user chose.
		stale := s.Name == "" || s.Name == s.Model || s.Name == s.Model+" (BLE)"
		if best != "" && stale && s.Name != best {
			a.slots[i].Name = best
		}
	}
}

// foldBLE re-merges every known BLE peripheral into the catalog and slots.
// The discovery callback can fire before the cloud catalog is loaded; folding
// again once it is warm closes that race and picks up friendly names.
func (a *App) foldBLE() {
	devs := a.ble.Devices()
	if len(devs) == 0 {
		return
	}
	a.mu.Lock()
	for _, d := range devs {
		a.mergeBLELocked(d)
	}
	a.mu.Unlock()
	a.scheduleStateEmit()
}

// bleMayReplaceLocked decides whether a BLE address may take over a slot's
// address. Empty or BLE always may; a LAN IP only if nothing has answered on
// it this run — e.g. a model with no LAN API carrying a stale WiFi address.
// If a later LAN scan proves the IP real, mergeLAN puts it back: LAN wins.
func (a *App) bleMayReplaceLocked(cur string) bool {
	cur = strings.TrimSpace(cur)
	if cur == "" || govee.IsBLE(cur) {
		return true
	}
	return !a.udp.SeenIP(cur)
}

func (a *App) refreshDevices() {
	a.mu.Lock()
	a.discovering = true
	a.discoverErr = ""
	a.mu.Unlock()
	a.emitState()

	var cloud []govee.Device
	key := a.apiKey()
	if key == "" {
		a.mu.Lock()
		a.discoverErr = "missing_key"
		a.mu.Unlock()
	} else {
		devs, err := govee.DiscoverCloud(key)
		if err != nil {
			a.mu.Lock()
			a.discoverErr = err.Error()
			a.mu.Unlock()
		} else {
			cloud = devs
		}
	}

	_ = a.udp.Scan()
	a.ble.Scan()
	time.Sleep(1200 * time.Millisecond)
	lan := a.udp.Devices()
	merged := govee.Merge(cloud, lan)
	bleDevs := a.ble.Devices()

	a.mu.Lock()
	a.catalog = merged
	byID := map[string]govee.Device{}
	for _, d := range merged {
		byID[govee.NormalizeID(d.ID)] = d
	}
	for i, s := range a.slots {
		id := govee.NormalizeID(s.DeviceID)
		if id == "" {
			continue
		}
		if d, ok := byID[id]; ok {
			if d.Name != "" {
				a.slots[i].Name = d.Name
			}
			if d.Model != "" {
				a.slots[i].Model = d.Model
			}
			if d.IP != "" {
				a.slots[i].IP = d.IP
			}
		}
	}
	// Rebuilding the catalog from cloud+LAN dropped the Bluetooth entries;
	// fold the known peripherals back in (BLE never overrides a LAN IP).
	for _, d := range bleDevs {
		a.mergeBLELocked(d)
	}
	a.discovering = false
	a.mu.Unlock()
	a.emitState()
}

// statusPollLoop asks every known lamp for its power/brightness once a second
// so the HUD mirrors the real world, including changes made from the Govee app
// or a physical switch.
func (a *App) statusPollLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("status poll recovered: %v", r)
		}
	}()
	t := time.NewTimer(statusPollActive)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}

		a.mu.Lock()
		// Hidden window or no recent interaction: the HUD is not being read,
		// so a slow heartbeat is enough to catch external changes.
		idle := a.hidden || time.Since(a.lastActivity) > statusPollActiveFor
		dancing := a.dancing
		ips := make([]string, 0, 9)
		for _, s := range a.slots {
			// BLE lamps have no status query yet; polling them would just
			// error into the log.
			if ip := strings.TrimSpace(s.IP); ip != "" && !govee.IsBLE(ip) {
				ips = append(ips, ip)
			}
		}
		a.mu.Unlock()

		// The animation is writing colours continuously; polling during it adds
		// traffic and tells us nothing we did not just set ourselves.
		if !dancing {
			for _, ip := range ips {
				_ = govee.QueryStatus(ip)
			}
		}

		next := statusPollActive
		if idle {
			next = statusPollIdle
		}
		t.Reset(next)
	}
}

// applyDeviceStatus folds a devStatus reply into HUD state: a lamp that is on
// belongs in the active pool, one that is off does not. Runs on the UDP read
// goroutine, so it must not block.
func (a *App) applyDeviceStatus(st govee.DevStatus) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("status apply recovered: %v", r)
		}
	}()
	a.mu.Lock()
	changed := false
	for i, s := range a.slots {
		if strings.TrimSpace(s.IP) != st.IP {
			continue
		}
		n := i + 1
		if a.pool == nil {
			a.pool = map[int]bool{}
		}
		// Ignore a reply that may predate a just-issued command.
		if t, ok := a.slotTouched[n]; ok && time.Since(t) < commandSettle {
			continue
		}
		if st.On != a.pool[n] {
			if st.On {
				a.pool[n] = true
			} else {
				delete(a.pool, n)
			}
			changed = true
		}
	}
	// Mirror the device's own brightness only while the user is not driving
	// the slider, so polling never fights an in-progress adjustment.
	if st.On && st.Brightness > 0 && time.Since(a.lastBrightTouch) > brightSyncGuard {
		if a.brightness != st.Brightness {
			a.brightness = st.Brightness
			a.pendingBright.Store(int32(st.Brightness))
			changed = true
		}
	}
	a.mu.Unlock()
	if changed {
		a.scheduleStateEmit()
	}
}

// ToggleDance starts or stops the slow colour fade across every pooled lamp.
// Bound to the star key. Each lamp walks the same palette from a different
// offset, so they stay related without ever showing the identical colour.
func (a *App) ToggleDance() HUDState {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("dance toggle recovered: %v", r)
		}
	}()
	a.recordUserActivity()
	a.mu.Lock()
	a.dancing = !a.dancing
	a.danceGen++
	gen := a.danceGen
	on := a.dancing
	a.mu.Unlock()

	if on {
		go a.danceLoop(gen)
	}
	a.emitState()
	a.emit("dance:toggle", on)
	return a.snapshot()
}

// Quit exits the process entirely. Bound to the period key, and distinct from
// Enter/HideHUD, which only hides the window and leaves Lightwave resident.
// The animation is stopped first so no colour write races the teardown; Wails
// then calls shutdown, which closes the MIDI and UDP listeners.
func (a *App) Quit() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("quit recovered: %v", r)
		}
	}()
	a.mu.Lock()
	a.dancing = false
	a.danceGen++
	a.mu.Unlock()

	ctx, ok := a.ctxOK()
	if !ok {
		return
	}
	runtime.Quit(ctx)
}

// Dancing reports whether the colour animation is running.
func (a *App) Dancing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dancing
}

func (a *App) danceLoop(gen int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("dance loop recovered: %v", r)
		}
	}()
	t := time.NewTicker(danceStep)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}

		a.mu.Lock()
		// A newer generation means the animation was restarted or stopped;
		// this goroutine must retire rather than fight the current one.
		if !a.dancing || a.danceGen != gen {
			a.mu.Unlock()
			return
		}
		pal := a.engine.Palette()
		ips := a.poolIPsLocked()
		a.mu.Unlock()

		if len(ips) == 0 {
			continue
		}
		phase := time.Since(start).Seconds() / dancePeriod.Seconds()
		for i, ip := range ips {
			// Offsetting each lamp by one swatch keeps the group in adjacent
			// parts of the palette: a moving gradient, not clones.
			c := pal.Walk(i, phase)
			_ = govee.SendColor(ip, c.R, c.G, c.B, c.Kelvin)
		}
	}
}

// dockReopenLoop restores the HUD when the user activates Lightwave while it
// has no visible window — which is what clicking the Dock tile does. Polling
// at 300ms keeps the response feeling immediate without measurable cost.
func (a *App) dockReopenLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("dock reopen recovered: %v", r)
		}
	}()
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	// Edge-triggered: activation only counts as a summon if the app was first
	// observed NOT frontmost after this hide. macOS occasionally fails to
	// complete the deactivation for frameless windows, and a level check
	// ("hidden and frontmost") would read that leftover focus as a Dock click
	// and pop the HUD straight back up.
	sawInactive := false
	var session time.Time
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.mu.Lock()
			hidden := a.hidden
			hiddenAt := a.hiddenAt
			a.mu.Unlock()
			if !hidden {
				sawInactive = false
				continue
			}
			if !hiddenAt.Equal(session) {
				// New hide session: previous observations do not carry over.
				session = hiddenAt
				sawInactive = false
			}
			if !appFrontmost() {
				sawInactive = true
				continue
			}
			if sawInactive {
				log.Printf("dock: reopen detected, showing HUD")
				a.ShowHUD()
			}
		}
	}
}

func (a *App) snapshot() HUDState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
}

func (a *App) snapshotLocked() HUDState {
	if len(a.slots) < 9 {
		next := make([]config.SlotBinding, 9)
		copy(next, a.slots)
		for i := len(a.slots); i < 9; i++ {
			next[i] = config.SlotBinding{Slot: i + 1}
		}
		a.slots = next
	}
	if a.pool == nil {
		a.pool = map[int]bool{}
	}
	views := make([]SlotView, 9)
	pool := make([]int, 0, 9)
	byID := map[string]govee.Device{}
	for _, d := range a.catalog {
		byID[govee.NormalizeID(d.ID)] = d
	}
	for i := 1; i <= 9; i++ {
		s := a.slots[i-1]
		v := SlotView{
			Number:   i,
			DeviceID: s.DeviceID,
			Name:     s.Name,
			Model:    s.Model,
			IP:       s.IP,
			Active:   a.pool[i],
			Online:   s.IP != "",
		}
		if d, ok := byID[govee.NormalizeID(s.DeviceID)]; ok {
			if v.Name == "" {
				v.Name = d.Name
			}
			if v.Model == "" {
				v.Model = d.Model
			}
			if d.IP != "" {
				v.IP = d.IP
				v.Online = true
			}
		}
		if v.Name == "" && v.DeviceID == "" {
			v.Name = "unmapped"
		}
		views[i-1] = v
		if a.pool[i] {
			pool = append(pool, i)
		}
	}
	ok, port := false, ""
	if a.midi != nil {
		ok, port = a.midi.Connected()
	} else {
		ok, port = a.midiOK, a.midiPort
	}
	return HUDState{
		Slots:         views,
		ActivePool:    pool,
		Brightness:    clampBrightness(a.brightness),
		PaletteIndex:  a.engine.Index,
		PaletteName:   a.engine.Name(),
		Dancing:       a.dancing,
		MIDIConnected: ok,
		MIDIPort:      port,
		DeviceCount:   len(a.catalog),
		NeedsSetup:    a.setupOpen,
		SetupOpen:     a.setupOpen,
		ConfigOpen:    a.setupOpen,
		HasAPIKey:     a.apiKeyLocked() != "",
		DiscoverError: a.discoverErr,
		Discovering:   a.discovering,
		FirstRun:      !a.configured,
		Catalog:       append([]govee.Device{}, a.catalog...),
		Hidden:        a.hidden,
		MappingPath:   config.MappingPath(),
		Settings:      a.settingsViewLocked(),
	}
}

func (a *App) apiKey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.apiKeyLocked()
}

func (a *App) apiKeyLocked() string {
	if k := config.EnvAPIKey(); k != "" {
		return k
	}
	return a.settings.GoveeAPIKey
}

func (a *App) settingsViewLocked() SettingsView {
	env := config.EnvAPIKey()
	return SettingsView{
		MidiCC:          a.settings.MidiCC,
		MidiCCAlt:       a.settings.MidiCCAlt,
		MidiNotePlus:    a.settings.MidiNotePlus,
		MidiNoteMinus:   a.settings.MidiNoteMinus,
		IdleHideSeconds: a.settings.IdleHideSeconds,
		HasEnvKey:       env != "",
		HasConfigKey:    a.settings.GoveeAPIKey != "",
		HasAPIKey:       env != "" || a.settings.GoveeAPIKey != "",
		EnvPath:         config.EnvFileHint(),
		ConfigPath:      config.SettingsPath(),
		MappingPath:     config.MappingPath(),
	}
}

func (a *App) emit(name string, data ...interface{}) {
	ctx, ok := a.ctxOK()
	if !ok {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("emit %s recovered: %v", name, r)
		}
	}()
	runtime.EventsEmit(ctx, name, data...)
}

func (a *App) emitState() {
	a.emit("state", a.snapshot())
}

func (a *App) GetState() HUDState {
	return a.snapshot()
}

func (a *App) GetSlots() []SlotView {
	return a.snapshot().Slots
}

func (a *App) GetDevices() []govee.Device {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]govee.Device(nil), a.catalog...)
}

func (a *App) GetActivePool() []int {
	return a.snapshot().ActivePool
}

func (a *App) GetPaletteIndex() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.engine.Index
}

func (a *App) Discover() HUDState {
	a.refreshDevices()
	return a.snapshot()
}

func (a *App) PingActivity() {
	a.recordUserActivity()
}

// PingMotion records pointer motion. With auto-hide gone it only keeps
// lastActivity fresh for the stale-drag reset; it must not cancel a
// deliberate hide, so it leaves fadeCancel alone.
func (a *App) PingMotion() {
	a.mu.Lock()
	a.lastActivity = time.Now()
	a.mu.Unlock()
}

func (a *App) MarkUIReady() {
	a.mu.Lock()
	a.uiReady = true
	if a.shownAt.IsZero() {
		a.shownAt = time.Now()
	}
	a.mu.Unlock()
}

func (a *App) noteShow() {
	a.mu.Lock()
	a.lastActivity = time.Now()
	a.fadeCancel++
	a.shownAt = time.Now()
	a.mu.Unlock()
	a.emit("activity")
}

func (a *App) recordUserActivity() {
	a.mu.Lock()
	a.lastActivity = time.Now()
	a.fadeCancel++
	a.mu.Unlock()
	a.emit("activity")
}

func (a *App) ToggleSlot(n int) error {
	if n < 1 || n > 9 {
		return fmt.Errorf("slot must be 1-9")
	}
	a.recordUserActivity()
	a.mu.Lock()
	if n > len(a.slots) {
		a.mu.Unlock()
		return fmt.Errorf("slot %d is unmapped", n)
	}
	id := a.slots[n-1].DeviceID
	if id == "" {
		a.mu.Unlock()
		return fmt.Errorf("slot %d is unmapped", n)
	}
	if a.pool == nil {
		a.pool = map[int]bool{}
	}
	if a.pool[n] {
		delete(a.pool, n)
	} else {
		a.pool[n] = true
	}
	active := a.pool[n]
	ip := a.slots[n-1].IP
	bright := clampBrightness(a.brightness)
	if a.slotTouched == nil {
		a.slotTouched = map[int]time.Time{}
	}
	a.slotTouched[n] = time.Now()
	a.mu.Unlock()
	if active && ip != "" {
		// Bring the lamp up at the level the slider is already showing, rather
		// than whatever brightness it happened to retain. Brightness is sent
		// after turn so the device is awake to receive it. A slider at the
		// bottom means "dimmest", not "off": only key 0 powers lights down.
		_ = sendTurn(ip, true)
		_ = govee.SendBrightness(ip, bright)
		// The pool changed, so the pump's "same value, skip it" shortcut no
		// longer reflects reality: a newly ignited lamp still needs the next
		// slider move even if the percentage has not changed.
		a.mu.Lock()
		a.lastSentBright = -1
		a.mu.Unlock()
		// Paint the whole pool from the current palette so the new lamp comes
		// up in palette colors and the group re-spreads to stay complementary.
		a.applyPaletteToPool()
	} else if !active && ip != "" {
		// Un-igniting a pad must actually extinguish the lamp; dropping it from
		// the pool only stops future slider updates reaching it.
		_ = sendTurn(ip, false)
	}
	a.emitState()
	a.emit("slot:toggle", n)
	return nil
}

// AllOff turns off every light currently in the active pool and empties the
// pool. Bound to key 0 / numpad 0. Devices without a LAN IP are skipped, the
// same as every other control path.
func (a *App) AllOff() HUDState {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("all off recovered: %v", r)
		}
	}()
	a.recordUserActivity()
	a.mu.Lock()
	// Turning everything off ends the animation; otherwise it would keep
	// writing colours to lamps the user just switched off.
	a.dancing = false
	a.danceGen++
	ips := a.poolIPsLocked()
	if a.slotTouched == nil {
		a.slotTouched = map[int]time.Time{}
	}
	now := time.Now()
	for n := range a.pool {
		a.slotTouched[n] = now
	}
	a.pool = map[int]bool{}
	// Keep the pump's idea of what was last sent in step with reality, so the
	// next slider move re-issues turn:on instead of assuming the lights are lit.
	a.lastSentBright = 0
	a.mu.Unlock()

	for _, ip := range ips {
		_ = sendTurn(ip, false)
	}
	a.emitState()
	a.emit("pool:alloff")
	return a.snapshot()
}

func clampBrightness(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func (a *App) SetBrightness(percent int) {
	a.applyBrightness(percent, true)
}

func (a *App) applyBrightness(percent int, emitNow bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("brightness recovered: %v", r)
		}
	}()
	percent = clampBrightness(percent)
	a.mu.Lock()
	a.brightness = percent
	a.lastBrightTouch = time.Now()
	a.mu.Unlock()
	a.pendingBright.Store(int32(percent))
	select {
	case a.brightKick <- struct{}{}:
	default:
	}
	if emitNow {
		a.emitState()
		return
	}
	a.scheduleStateEmit()
}

func (a *App) brightnessPump() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("brightness pump recovered: %v", r)
		}
	}()
	for {
		select {
		case <-a.stop:
			return
		case <-a.brightKick:
		}
		// Coalesce a burst of slider events into one send.
		time.Sleep(brightCoalesce)
	drain:
		for {
			select {
			case <-a.brightKick:
			default:
				break drain
			}
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("brightness apply recovered: %v", r)
				}
			}()
			percent := clampBrightness(int(a.pendingBright.Load()))
			a.mu.Lock()
			a.brightness = percent
			ips := a.poolIPsLocked()
			wasOff := a.lastSentBright == 0
			unchanged := a.lastSentBright == percent
			a.lastSentBright = percent
			a.mu.Unlock()
			if len(ips) == 0 || unchanged {
				return
			}
			sendPoolBrightness(ips, percent, wasOff && percent > 0)
		}()
		// Hard floor between LAN writes, so a fast slide cannot flood the
		// devices. A value arriving during this window is not lost: it stays
		// in pendingBright and the next loop picks up the latest one.
		time.Sleep(brightMinInterval)
	}
}

// applyPaletteToPool paints every pooled lamp with the current palette's
// spread. Skipped while the dance animation owns the colors.
func (a *App) applyPaletteToPool() {
	a.mu.Lock()
	if a.dancing {
		a.mu.Unlock()
		return
	}
	ips := a.poolIPsLocked()
	swatches := a.engine.Distribute(len(ips))
	a.mu.Unlock()
	if len(swatches) == 0 {
		return
	}
	for i, ip := range ips {
		c := swatches[0]
		if i < len(swatches) {
			c = swatches[i]
		}
		_ = govee.SendColor(ip, c.R, c.G, c.B, c.Kelvin)
	}
}

func (a *App) CycleColor(direction int) HUDState {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("color cycle recovered: %v", r)
		}
	}()
	if direction == 0 {
		direction = 1
	}
	a.recordUserActivity()
	a.mu.Lock()
	pal := a.engine.Cycle(direction)
	ips := a.poolIPsLocked()
	swatches := a.engine.Distribute(len(ips))
	a.mu.Unlock()
	for i, ip := range ips {
		c := swatches[0]
		if i < len(swatches) {
			c = swatches[i]
		}
		_ = govee.SendTurn(ip, true)
		_ = govee.SendColor(ip, c.R, c.G, c.B, c.Kelvin)
	}
	a.emitState()
	a.emit("color:cycle", pal.Name)
	return a.snapshot()
}

func (a *App) poolIPsLocked() []string {
	ips := make([]string, 0, 9)
	if a.pool == nil {
		return ips
	}
	limit := len(a.slots)
	if limit > 9 {
		limit = 9
	}
	for n := 1; n <= limit; n++ {
		if !a.pool[n] {
			continue
		}
		ip := strings.TrimSpace(a.slots[n-1].IP)
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}

func (a *App) OpenSetup() {
	a.OpenConfig()
}

func (a *App) OpenConfig() {
	a.mu.Lock()
	a.setupOpen = true
	a.mu.Unlock()
	a.noteShow()
	a.sizeForMode(true)
	a.emitState()
	a.emit("hud:shown")
}

// MoveSlot relocates the binding on pad `from` to pad `to`. If the target pad
// already holds a light the two swap, so a drag can never silently discard a
// binding. Pool membership follows the lights to their new pads.
func (a *App) MoveSlot(from, to int) (HUDState, error) {
	if from < 1 || from > 9 || to < 1 || to > 9 {
		return a.snapshot(), fmt.Errorf("pads must be 1-9")
	}
	if from == to {
		return a.snapshot(), nil
	}
	a.mu.Lock()
	if a.slots[from-1].DeviceID == "" {
		st := a.snapshotLocked()
		a.mu.Unlock()
		return st, fmt.Errorf("pad %d is empty", from)
	}
	a.slots[from-1], a.slots[to-1] = a.slots[to-1], a.slots[from-1]
	a.slots[from-1].Slot = from
	a.slots[to-1].Slot = to
	if a.pool == nil {
		a.pool = map[int]bool{}
	}
	fromLit, toLit := a.pool[from], a.pool[to]
	delete(a.pool, from)
	delete(a.pool, to)
	if fromLit {
		a.pool[to] = true
	}
	if toLit && a.slots[from-1].DeviceID != "" {
		a.pool[from] = true
	}
	st := a.snapshotLocked()
	a.mu.Unlock()
	a.emitState()
	return st, nil
}

func (a *App) FillRemaining() HUDState {
	a.mu.Lock()
	used := map[string]bool{}
	for _, s := range a.slots {
		if s.DeviceID != "" {
			used[govee.NormalizeID(s.DeviceID)] = true
		}
	}
	unused := make([]govee.Device, 0)
	for _, d := range a.catalog {
		id := govee.NormalizeID(d.ID)
		if id == "" || used[id] {
			continue
		}
		unused = append(unused, d)
	}
	ui := 0
	for i := range a.slots {
		if a.slots[i].DeviceID != "" || ui >= len(unused) {
			continue
		}
		d := unused[ui]
		ui++
		a.slots[i].DeviceID = d.ID
		a.slots[i].Name = d.Name
		a.slots[i].Model = d.Model
		a.slots[i].IP = d.IP
	}
	a.mu.Unlock()
	a.emitState()
	return a.snapshot()
}

func (a *App) AssignSlot(slot int, deviceID string) (HUDState, error) {
	if slot < 1 || slot > 9 {
		return a.snapshot(), fmt.Errorf("slot must be 1-9")
	}
	id := govee.NormalizeID(deviceID)
	a.mu.Lock()
	if id != "" {
		for i, s := range a.slots {
			if i == slot-1 {
				continue
			}
			if govee.NormalizeID(s.DeviceID) == id {
				name := s.Name
				if name == "" {
					name = id
				}
				st := a.snapshotLocked()
				a.mu.Unlock()
				return st, fmt.Errorf("already on pad %d (%s)", s.Slot, name)
			}
		}
	}
	bind := config.SlotBinding{Slot: slot, DeviceID: id}
	if id != "" {
		for _, d := range a.catalog {
			if govee.NormalizeID(d.ID) == id {
				bind.Name = d.Name
				bind.Model = d.Model
				bind.IP = d.IP
				break
			}
		}
	}
	a.slots[slot-1] = bind
	delete(a.pool, slot)
	st := a.snapshotLocked()
	a.mu.Unlock()
	a.emitState()
	return st, nil
}

func (a *App) CancelSetup() error {
	return a.CloseConfig()
}

func (a *App) CloseConfig() error {
	a.mu.Lock()
	if !a.configured {
		a.mu.Unlock()
		return fmt.Errorf("save the pad map before leaving config")
	}
	a.setupOpen = false
	a.mu.Unlock()
	a.sizeForMode(false)
	a.noteShow()
	a.emitState()
	return nil
}

func (a *App) SaveMappings(slots []config.SlotBinding) error {
	seen := map[string]int{}
	normalized := config.SlotFile{Configured: true, Slots: make([]config.SlotBinding, 9)}
	for i := 0; i < 9; i++ {
		normalized.Slots[i] = config.SlotBinding{Slot: i + 1}
	}
	for _, s := range slots {
		if s.Slot < 1 || s.Slot > 9 {
			continue
		}
		id := govee.NormalizeID(s.DeviceID)
		if id != "" {
			if other, ok := seen[id]; ok {
				return fmt.Errorf("device mapped to both pad %d and pad %d", other, s.Slot)
			}
			seen[id] = s.Slot
		}
		s.DeviceID = id
		s.Slot = s.Slot
		normalized.Slots[s.Slot-1] = s
	}
	a.mu.Lock()
	byID := map[string]govee.Device{}
	for _, d := range a.catalog {
		byID[govee.NormalizeID(d.ID)] = d
	}
	for i, s := range normalized.Slots {
		d, ok := byID[govee.NormalizeID(s.DeviceID)]
		if !ok {
			continue
		}
		if s.Name == "" {
			s.Name = d.Name
		}
		if s.Model == "" {
			s.Model = d.Model
		}
		if s.IP == "" {
			s.IP = d.IP
		}
		normalized.Slots[i] = s
	}
	a.mu.Unlock()
	if err := config.SaveSlotFile(normalized); err != nil {
		return err
	}
	a.mu.Lock()
	a.slots = normalized.Slots
	a.configured = true
	a.pool = map[int]bool{}
	a.mu.Unlock()
	a.emitState()
	go a.refreshDevices()
	return nil
}

func (a *App) CommitMappings() error {
	a.mu.Lock()
	slots := append([]config.SlotBinding(nil), a.slots...)
	a.mu.Unlock()
	return a.SaveMappings(slots)
}

func (a *App) SaveSettings(in SettingsView) error {
	a.mu.Lock()
	s := a.settings
	s.MidiCC = in.MidiCC
	s.MidiCCAlt = in.MidiCCAlt
	s.MidiNotePlus = in.MidiNotePlus
	s.MidiNoteMinus = in.MidiNoteMinus
	s.IdleHideSeconds = in.IdleHideSeconds
	a.mu.Unlock()
	if err := config.SaveSettings(s); err != nil {
		return err
	}
	a.mu.Lock()
	a.settings = s
	a.midiCfg = s.MIDI()
	if a.midi != nil {
		a.midi.Update(a.midiCfg)
	}
	a.mu.Unlock()
	a.emitState()
	return nil
}

func (a *App) SetConfigAPIKey(key string) error {
	a.mu.Lock()
	s := a.settings
	s.GoveeAPIKey = strings.TrimSpace(key)
	a.settings = s
	a.mu.Unlock()
	if err := config.SaveSettings(s); err != nil {
		return err
	}
	a.emitState()
	go a.refreshDevices()
	return nil
}

func (a *App) ScanLAN() HUDState {
	a.mu.Lock()
	a.discovering = true
	a.mu.Unlock()
	a.emitState()
	_ = a.udp.Scan()
	a.ble.Scan()
	time.Sleep(1200 * time.Millisecond)
	lan := a.udp.Devices()
	bleDevs := a.ble.Devices()
	a.mu.Lock()
	a.catalog = govee.Merge(append([]govee.Device(nil), a.catalog...), lan)
	for _, d := range bleDevs {
		a.mergeBLELocked(d)
	}
	a.discovering = false
	a.mu.Unlock()
	a.emitState()
	return a.snapshot()
}

func (a *App) sizeForMode(setup bool) {
	ctx, ok := a.ctxOK()
	if !ok {
		return
	}
	// Resizing and re-centering are only needed when the mode actually
	// changes. Doing them on every show made each reappearance pay three
	// synchronous AppKit calls — and snapped a dragged window back to center.
	mode := 1
	if setup {
		mode = 2
	}
	a.mu.Lock()
	same := a.sizedMode == mode
	a.sizedMode = mode
	a.mu.Unlock()
	if same {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("window size recovered: %v", r)
		}
	}()
	runtime.WindowSetMinSize(ctx, WindowMinW, WindowMinH)
	if setup {
		runtime.WindowSetSize(ctx, ConfigW, ConfigH)
	} else {
		runtime.WindowSetSize(ctx, HUDW, HUDH)
	}
	// Re-center after a resize so the window does not drift toward a corner
	// when it grows.
	runtime.WindowCenter(ctx)
}

func (a *App) ShowHUD() {
	ctx, ok := a.ctxOK()
	if !ok {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("window show recovered: %v", r)
		}
	}()
	a.mu.Lock()
	setup := a.setupOpen
	a.hidden = false
	a.hiding = false
	a.mu.Unlock()
	a.sizeForMode(setup)
	// Restore opacity before the window is on screen, so it appears with the
	// UI already fully drawn instead of fading in after the fact.
	a.emit("hud:shown")
	a.emitState()
	runtime.WindowShow(ctx)
	runtime.WindowSetAlwaysOnTop(ctx, true)
	// WindowShow unhides the window but does not activate the process; without
	// this the HUD can reappear behind whatever the user was working in.
	activateApp()
	a.noteShow()
}

func (a *App) HideHUD() {
	if _, ok := a.ctxOK(); !ok {
		return
	}
	a.mu.Lock()
	if a.hidden || a.hiding || a.setupOpen || a.dragging {
		a.mu.Unlock()
		return
	}
	a.hiding = true
	token := a.fadeCancel
	a.mu.Unlock()
	a.emit("hud:fade-out")
	go func() {
		// Just long enough for the quick CSS fade to land; anything slower
		// reads as the UI dismantling itself piece by piece.
		time.Sleep(90 * time.Millisecond)
		a.mu.Lock()
		if a.fadeCancel != token || a.setupOpen || a.dragging {
			a.hiding = false
			a.mu.Unlock()
			a.emit("hud:shown")
			return
		}
		a.hidden = true
		a.hiddenAt = time.Now()
		a.hiding = false
		a.mu.Unlock()
		a.safeWindowHide()
		a.emitState()
	}()
}

func (a *App) safeWindowHide() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("window hide recovered: %v", r)
		}
	}()
	ctx, ok := a.ctxOK()
	if !ok {
		return
	}
	runtime.WindowHide(ctx)
	// Hand focus back to the previous app. Without this Lightwave stays
	// frontmost after the window vanishes, and the Dock-reopen watcher would
	// read that leftover focus as a summon and re-show the HUD immediately.
	hideApp()
}

func (a *App) StartWindowDrag() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("window drag recovered: %v", r)
		}
	}()
	a.mu.Lock()
	a.dragging = true
	a.lastActivity = time.Now()
	a.fadeCancel++
	if a.hiding {
		a.hiding = false
		a.hidden = false
	}
	a.mu.Unlock()
	startNativeWindowDrag()
}

func (a *App) EndWindowDrag() {
	a.mu.Lock()
	a.dragging = false
	a.lastActivity = time.Now()
	a.mu.Unlock()
}

func (a *App) ToggleWindow() {
	a.mu.Lock()
	hidden := a.hidden
	a.mu.Unlock()
	if hidden {
		a.ShowHUD()
		return
	}
	a.HideHUD()
}

// inactivityLoop no longer hides anything: the HUD stays up until the user
// dismisses it (Enter, Stream Deck toggle, or --toggle). It only clears a
// stale window-drag flag if a drag never received its pointerup.
func (a *App) inactivityLoop() {
	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		a.mu.Lock()
		if a.dragging && time.Since(a.lastActivity) > 2*time.Second {
			a.dragging = false
		}
		a.mu.Unlock()
	}
}
