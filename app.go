package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lightwave/internal/color"
	"lightwave/internal/config"
	"lightwave/internal/govee"
	"lightwave/internal/ipc"
	midilstn "lightwave/internal/midi"
	"lightwave/internal/web"

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
	Slots           []SlotView     `json:"slots"`
	ActivePool      []int          `json:"activePool"`
	Brightness      int            `json:"brightness"`
	PaletteIndex    int            `json:"paletteIndex"`
	PaletteName     string         `json:"paletteName"`
	MIDIConnected   bool           `json:"midiConnected"`
	MIDIPort        string         `json:"midiPort"`
	DeviceCount     int            `json:"deviceCount"`
	NeedsSetup      bool           `json:"needsSetup"`
	SetupOpen       bool           `json:"setupOpen"`
	HasAPIKey       bool           `json:"hasApiKey"`
	DiscoverError   string         `json:"discoverError"`
	Discovering     bool           `json:"discovering"`
	FirstRun        bool           `json:"firstRun"`
	Catalog         []govee.Device `json:"catalog"`
	Hidden          bool           `json:"hidden"`
	MappingPath     string         `json:"mappingPath"`
	ConfigOpen      bool           `json:"configOpen"`
	MapDirty        bool           `json:"mapDirty"`
	Dancing         bool           `json:"dancing"`
	Gradient        bool           `json:"gradient"`
	BleScanning     bool           `json:"bleScanning"`
	BluetoothDenied bool           `json:"bluetoothDenied"`
	BluetoothOff    bool           `json:"bluetoothOff"`
	Settings        SettingsView   `json:"settings"`
}

type SettingsView struct {
	MidiCC          int      `json:"midiCC"`
	MidiCCAlt       int      `json:"midiCCAlt"`
	MidiNotePlus    int      `json:"midiNotePlus"`
	MidiNoteMinus   int      `json:"midiNoteMinus"`
	MidiCCMin       int      `json:"midiCCMin"`
	MidiCCMax       int      `json:"midiCCMax"`
	IdleHideSeconds int      `json:"idleHideSeconds"`
	HasEnvKey       bool     `json:"hasEnvKey"`
	HasConfigKey    bool     `json:"hasConfigKey"`
	HasAPIKey       bool     `json:"hasApiKey"`
	EnvPath         string   `json:"envPath"`
	ConfigPath      string   `json:"configPath"`
	MappingPath     string   `json:"mappingPath"`
	WebEnabled      bool     `json:"webEnabled"`
	LaunchAtLogin   bool     `json:"launchAtLogin"`
	WebAddr         string   `json:"webAddr"`
	WebRunning      bool     `json:"webRunning"`
	WebHasToken     bool     `json:"webHasToken"`
	WebURLs         []string `json:"webUrls"`
}

type App struct {
	// ctx is written once by startup and read by every background goroutine
	// (MIDI drain, brightness pump, idle loop, IPC). It must be accessed only
	// through ctxOK/setCtx: handing Wails a context that lacks "events" or
	// "frontend" makes the runtime call log.Fatalf — an immediate os.Exit
	// that no recover() can stop. Stored by value (atomic.Value), never as a
	// pointer to a function parameter: that pointer can dangle after startup
	// returns and later ShowHUD/emit calls then kill the process on load.
	ctxVal atomic.Value // context.Context

	mu         sync.Mutex
	slots      []config.SlotBinding
	configured bool
	setupOpen  bool
	// mapDirty is set by pad edits that live only in memory (AssignSlot,
	// MoveSlot) and cleared by CommitMappings. Escape uses it to decide
	// whether leaving config would discard work.
	mapDirty   bool
	forceSetup bool
	pool       map[int]bool
	// lastPool is the set of pads that were lit the last time the pool went
	// empty, so a controller can bring back exactly that scene rather than
	// every bound light. Survives until something is lit again.
	lastPool     map[int]bool
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
	// hideGen increments only when the window is deliberately shown or
	// dragged. HideHUD captures it so an in-flight fade can be aborted by
	// ShowHUD — not by clicks, keys, or MIDI, which used to emit hud:shown
	// 90ms after hud:fade-out and flicker the shell.
	hideGen   int
	sizedMode int // 0 never sized, 1 HUD, 2 config

	ipcSrv       atomic.Pointer[ipc.Server]
	lastRemoteMu sync.Mutex
	lastRemote   string
	udp          *govee.UDP
	ble          *govee.BLE
	// webSrv serves the HUD to phones; webAssets is the same embedded bundle
	// the desktop window runs, so hosting it costs no extra memory.
	webSrv    *web.Server
	webAssets fs.FS
	midi      *midilstn.Listener
	midiCfg   config.MIDI
	settings  config.Settings

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
	// gradient selects the scene style: false paints every pooled lamp the
	// same palette colour; true spreads complementary/adjacent swatches
	// across the pool. RGBIC strips also get a zone ramp in gradient mode.
	gradient  bool
	lastColor map[string]color.RGBK
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
		gradient:     settings.Gradient,
		lastColor:    map[string]color.RGBK{},
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
	for _, s := range a.slots {
		if s.IP != "" && s.Model != "" {
			govee.Remember(s.IP, s.Model)
		}
	}
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
	a.ctxVal.Store(ctx)
}

// ctxOK returns the lifecycle context and whether it is safe to hand to the
// Wails runtime. WindowShow/Hide/Quit need "frontend"; EventsEmit needs
// "events". Either missing makes the runtime call log.Fatalf and kill the
// process, so both are required before any runtime call.
func (a *App) ctxOK() (context.Context, bool) {
	v := a.ctxVal.Load()
	if v == nil {
		return nil, false
	}
	ctx, ok := v.(context.Context)
	if !ok || ctx == nil {
		return nil, false
	}
	if ctx.Value("events") == nil || ctx.Value("frontend") == nil {
		return nil, false
	}
	return ctx, true
}

func (a *App) startup(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("startup recovered: %v", r)
		}
	}()
	a.setCtx(ctx)
	// Env and settings were already loaded in main before NewApp; nothing can
	// have changed them since, so re-walking the .env search paths here only
	// delayed first paint.

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
	a.ble.OnAdapter(func() { a.emitState() })
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

	if c, ok := a.ctxOK(); ok {
		runtime.WindowShow(c)
		runtime.WindowCenter(c)
	}
	a.noteShow()

	// Clicking the Dock icon while the HUD is hidden must bring it back.
	// AppKit's reopen event has no Wails hook, so watch for its signature
	// instead: the app becoming frontmost with no visible window.
	go a.dockReopenLoop()

	if a.settingsSnapshot().WebEnabled {
		if err := a.startWebServer(); err != nil {
			log.Printf("web: %v", err)
		}
	}

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
	if a.webSrv != nil {
		_ = a.webSrv.Stop()
	}
}

// settingsSnapshot copies the settings under the lock.
func (a *App) settingsSnapshot() config.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

// MIDI events land in atomics on the CGO thread and are polled here. 8ms keeps
// a knob turn feeling instant, but paying 125 wakeups/sec forever — hidden,
// idle, or with no MIDI hardware at all — is pure battery drain. After a quiet
// stretch the poll backs off; the first event after idle waits at most one
// slow tick (below perception for a key press) and snaps the rate back up.
const (
	midiPollFast    = 8 * time.Millisecond
	midiPollSlow    = 60 * time.Millisecond
	midiPollFastFor = 2 * time.Second
)

func (a *App) midiApplyLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("midi: apply loop recovered: %v", r)
		}
	}()
	t := time.NewTimer(midiPollFast)
	defer t.Stop()
	lastEvent := time.Now()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
		if a.drainMIDI() {
			lastEvent = time.Now()
		}
		next := midiPollFast
		if time.Since(lastEvent) > midiPollFastFor {
			next = midiPollSlow
		}
		t.Reset(next)
	}
}

// drainMIDI applies pending MIDI input and reports whether anything arrived.
func (a *App) drainMIDI() bool {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("midi: drain recovered: %v", r)
		}
	}()
	if a.midi == nil {
		return false
	}
	activity := false
	if ok, port, changed := a.midi.TakeStatus(); changed {
		a.mu.Lock()
		a.midiOK = ok
		a.midiPort = port
		a.mu.Unlock()
		a.emitState()
		activity = true
	}
	if _, val, ok := a.midi.TakeCC(); ok {
		// Brightness only. Never ShowHUD/HideHUD/ToggleWindow from MIDI.
		a.applyBrightness(midilstn.CCToPercent(val), false)
		activity = true
	}
	if note, viaCC, ok := a.midi.TakeNote(); ok {
		a.mu.Lock()
		plus, minus := a.midiCfg.NotePlus, a.midiCfg.NoteMinus
		a.mu.Unlock()
		if dir, hit := midilstn.PaletteDelta(note, plus, minus); hit {
			kind := "note"
			if viaCC {
				kind = "cc"
			}
			log.Printf("midi: %s %d → CycleColor(%d)", kind, note, dir)
			a.CycleColor(dir)
		}
		activity = true
	}
	return activity
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
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ipc: %q recovered: %v", cmd, r)
		}
	}()
	switch cmd {
	case "SETUP", "CONFIG":
		a.OpenConfig()
		a.ShowHUD()
	case "SHOW":
		a.ShowHUD()
	case "TOGGLE":
		// Stream Deck / `lightwave --toggle` only. Unknown socket lines must
		// not hide the window — a stuck sender would flicker hide/show.
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
			govee.Remember(a.slots[i].IP, a.slots[i].Model)
		}
	}
	if d.IP != "" {
		govee.Remember(d.IP, d.Model)
	}
}

// mergeBLE folds a Bluetooth discovery into the catalog and slots. A BLE
// address is a fallback link: it never replaces a working LAN IP. When the
// peripheral can be matched to a cloud identity — same model, and the MAC
// tail from its advertised name appearing inside the cloud device ID — it
// adopts that entry (and its friendly name) instead of duplicating it.
func (a *App) mergeBLE(d govee.Device) {
	a.mu.Lock()
	changed := a.mergeBLELocked(d)
	var snap config.SlotFile
	if changed {
		snap = config.SlotFile{Configured: a.configured, Slots: append([]config.SlotBinding(nil), a.slots...)}
	}
	a.mu.Unlock()
	if changed {
		if err := config.SaveSlotFile(snap); err != nil {
			log.Printf("govee ble: persist remapped pad: %v", err)
		}
	}
}

func (a *App) mergeBLELocked(d govee.Device) bool {
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
		if d.AdvName != "" {
			c.AdvName = d.AdvName
		}
		c.Online = true
		placeholder := c.Name == "" || c.Name == "Govee BLE" || c.Name == c.Model || c.Name == c.Model+" (BLE)"
		if d.Name != "" && d.Name != "Govee BLE" && placeholder {
			c.Name = d.Name
		}
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
	slotDirty := false
	for i, s := range a.slots {
		sameID := govee.NormalizeID(s.DeviceID) == id
		tail := govee.BLEBindingMatch(d.Model, d.AdvName, s.Model, s.DeviceID, s.Name, s.Custom)
		if !sameID && !tail {
			continue
		}
		if !sameID && tail {
			taken := false
			for j, o := range a.slots {
				if i != j && govee.NormalizeID(o.DeviceID) == id {
					taken = true
					break
				}
			}
			if !taken && (s.DeviceID == "" || strings.HasPrefix(strings.ToUpper(s.DeviceID), "BLE")) {
				log.Printf("govee ble: pad %d remapped id %s → %s", i+1, s.DeviceID, id)
				a.slots[i].DeviceID = id
				slotDirty = true
			}
		}
		if a.bleMayReplaceLocked(s.IP) {
			if a.slots[i].IP != d.IP {
				log.Printf("govee ble: pad %d (%s) bluetooth %s", i+1, s.Name, d.IP)
				slotDirty = true
			}
			a.slots[i].IP = d.IP
		}
		if a.slots[i].Model == "" {
			a.slots[i].Model = d.Model
		}
		govee.Remember(a.slots[i].IP, a.slots[i].Model)
		// Upgrade placeholder names ("H617A", "H617A (BLE)") to the best we
		// have — the cloud name for matched lamps, the suffixed fallback
		// otherwise — but never touch a name the user chose.
		stale := s.Name == "" || s.Name == s.Model || s.Name == s.Model+" (BLE)"
		if best != "" && stale && s.Name != best {
			a.slots[i].Name = best
		}
	}
	if d.IP != "" {
		govee.Remember(d.IP, d.Model)
	}
	return slotDirty
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
	changed := false
	for _, d := range devs {
		if a.mergeBLELocked(d) {
			changed = true
		}
	}
	var snap config.SlotFile
	if changed {
		snap = config.SlotFile{Configured: a.configured, Slots: append([]config.SlotBinding(nil), a.slots...)}
	}
	a.mu.Unlock()
	if changed {
		if err := config.SaveSlotFile(snap); err != nil {
			log.Printf("govee ble: persist remapped pad: %v", err)
		}
	}
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
		reported := normalizeReportedBrightness(st.Brightness)
		if a.brightness != reported {
			a.brightness = reported
			a.pendingBright.Store(int32(reported))
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
		dests := a.poolDestsLocked()
		grad := a.gradient
		a.mu.Unlock()

		if len(dests) == 0 {
			continue
		}
		phase := time.Since(start).Seconds() / dancePeriod.Seconds()
		for i, d := range dests {
			if grad && govee.SupportsSegments(d.Model) {
				lampStep := 1.0 / float64(len(pal.Colors))
				_ = govee.SendGradient(d.IP, pal.GradientAt(phase+float64(i)*lampStep, govee.GradientBands))
				continue
			}
			offset := 0
			if grad {
				offset = i
			}
			c := pal.Walk(offset, phase)
			_ = govee.SendColor(d.IP, c.R, c.G, c.B, 0)
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
			hiding := a.hiding
			hiddenAt := a.hiddenAt
			a.mu.Unlock()
			if !hidden || hiding {
				sawInactive = false
				continue
			}
			// hideApp() plus AlwaysOnTop can bounce activation for a beat.
			// Treat that leftover focus as part of the hide, not a Dock click.
			if time.Since(hiddenAt) < time.Second {
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
			Name:     s.Label(),
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
		Slots:           views,
		ActivePool:      pool,
		Brightness:      clampBrightness(a.brightness),
		PaletteIndex:    a.engine.Index,
		PaletteName:     a.engine.Name(),
		Dancing:         a.dancing,
		Gradient:        a.gradient,
		MIDIConnected:   ok,
		MIDIPort:        port,
		DeviceCount:     len(a.catalog),
		NeedsSetup:      a.setupOpen,
		SetupOpen:       a.setupOpen,
		ConfigOpen:      a.setupOpen,
		MapDirty:        a.mapDirty,
		HasAPIKey:       a.apiKeyLocked() != "",
		DiscoverError:   a.discoverErr,
		Discovering:     a.discovering,
		FirstRun:        !a.configured,
		Catalog:         append([]govee.Device{}, a.catalog...),
		Hidden:          a.hidden,
		MappingPath:     config.MappingPath(),
		BleScanning:     a.ble != nil && a.ble.Scanning(),
		BluetoothDenied: a.ble != nil && a.ble.Unauthorized(),
		BluetoothOff:    a.ble != nil && a.ble.PoweredOff(),
		Settings:        a.settingsViewLocked(),
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
	running := a.webSrv != nil && a.webSrv.Running()
	var urls []string
	if running {
		urls = web.URLs(a.webSrv.Addr())
	}
	return SettingsView{
		MidiCC:          a.settings.MidiCC,
		MidiCCAlt:       a.settings.MidiCCAlt,
		MidiNotePlus:    a.settings.MidiNotePlus,
		MidiNoteMinus:   a.settings.MidiNoteMinus,
		MidiCCMin:       a.settings.MidiCCMin,
		MidiCCMax:       a.settings.MidiCCMax,
		IdleHideSeconds: a.settings.IdleHideSeconds,
		HasEnvKey:       env != "",
		HasConfigKey:    a.settings.GoveeAPIKey != "",
		HasAPIKey:       env != "" || a.settings.GoveeAPIKey != "",
		EnvPath:         config.EnvFileHint(),
		ConfigPath:      config.SettingsPath(),
		MappingPath:     config.MappingPath(),
		// Read the agent from disk rather than trusting the saved flag: the
		// user can remove it in System Settings, and the toggle should show
		// what is actually installed.
		LaunchAtLogin: config.LoginItemEnabled(),
		WebEnabled:    a.settings.WebEnabled,
		WebAddr:       a.settings.WebAddr,
		WebHasToken:   a.settings.WebToken != "",
		WebRunning:    running,
		WebURLs:       urls,
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
	st := a.snapshot()
	a.emit("state", st)
	// External controllers (the Stream Deck plugin) subscribe over the IPC
	// socket; pushing here means their keys track the HUD, the numpad, and
	// status polling without any extra plumbing at each call site.
	a.publishRemoteState()
	// Browsers get the same push. Only "state" is forwarded: the window
	// events (hud:fade-out and friends) describe the desktop window, and
	// replaying them would fade a phone screen to black when the desktop HUD
	// is dismissed.
	if a.webSrv != nil && a.webSrv.Running() {
		// Reuse the snapshot already taken above rather than locking again.
		a.webSrv.Publish("state", webStateFrom(st))
	}
}

// SetIPCServer hands the app the socket server so it can answer remote
// commands and push state. Safe before or after startup.
func (a *App) SetIPCServer(s *ipc.Server) {
	a.ipcSrv.Store(s)
	s.SetReplier(a.RemoteCommand)
}

func (a *App) ipcServer() *ipc.Server { return a.ipcSrv.Load() }

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

// PingMotion records pointer motion. It must not abort an in-flight hide.
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
	a.hideGen++
	a.shownAt = time.Now()
	a.mu.Unlock()
}

func (a *App) recordUserActivity() {
	a.mu.Lock()
	a.lastActivity = time.Now()
	a.mu.Unlock()
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
		// Snapshot first: if this is the last lit pad, the scene about to go
		// dark is what a recall should bring back.
		a.rememberPoolLocked()
		delete(a.pool, n)
	} else {
		a.pool[n] = true
	}
	active := a.pool[n]
	ip, model, label := a.slotLinkLocked(n)
	if ip != "" && ip != a.slots[n-1].IP {
		log.Printf("pad %d %s using live address %s (slot had %s)", n, label, ip, a.slots[n-1].IP)
		a.slots[n-1].IP = ip
		if model != "" {
			a.slots[n-1].Model = model
		}
	}
	bright := ignitedBrightness(a.brightness)
	if a.slotTouched == nil {
		a.slotTouched = map[int]time.Time{}
	}
	a.slotTouched[n] = time.Now()
	a.mu.Unlock()
	log.Printf("pad %d %s ignite=%v ip=%q model=%q", n, label, active, ip, model)
	if ip != "" && model != "" {
		govee.Remember(ip, model)
	}
	if govee.IsBLE(ip) {
		a.ble.Scan()
	}
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
	} else if ip == "" {
		log.Printf("pad %d %s has no address — cannot send power", n, label)
	}
	a.emitState()
	a.emit("slot:toggle", n)
	return nil
}

// AllOff turns off every light currently in the active pool and empties the
// pool. Bound to key 0 / numpad 0. Devices without a LAN IP are skipped, the
// same as every other control path.
// ToggleAll switches every bound light off, or — when nothing is lit — brings
// them all back on at the current slider level and palette. Key 0 and the
// Stream Deck "All Lights" action both land here.
func (a *App) ToggleAll() HUDState {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("toggle all recovered: %v", r)
		}
	}()
	a.mu.Lock()
	anyLit := len(a.pool) > 0
	a.mu.Unlock()
	if anyLit {
		return a.AllOff()
	}
	return a.AllOn()
}

// AllOn ignites every bound light, matching what pressing each pad would do:
// power on, brightness at the slider level, then the palette spread across the
// group so no two lamps come up identical.
func (a *App) AllOn() HUDState {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("all on recovered: %v", r)
		}
	}()
	a.recordUserActivity()
	a.mu.Lock()
	if a.pool == nil {
		a.pool = map[int]bool{}
	}
	if a.slotTouched == nil {
		a.slotTouched = map[int]time.Time{}
	}
	now := time.Now()
	type target struct {
		n  int
		ip string
	}
	var targets []target
	limit := len(a.slots)
	if limit > 9 {
		limit = 9
	}
	for n := 1; n <= limit; n++ {
		s := a.slots[n-1]
		ip, model, _ := a.slotLinkLocked(n)
		if s.DeviceID == "" || ip == "" {
			continue
		}
		if model != "" {
			govee.Remember(ip, model)
		}
		a.pool[n] = true
		a.slotTouched[n] = now
		targets = append(targets, target{n: n, ip: ip})
	}
	bright := ignitedBrightness(a.brightness)
	// The pool changed, so the pump's "same value, skip it" shortcut no longer
	// reflects reality.
	a.lastSentBright = -1
	a.mu.Unlock()

	for _, t := range targets {
		_ = sendTurn(t.ip, true)
		_ = govee.SendBrightness(t.ip, bright)
	}
	a.applyPaletteToPool()
	a.emitState()
	a.emit("pool:allon")
	return a.snapshot()
}

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
	a.rememberPoolLocked()
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

// rememberPoolLocked snapshots the lit pads before they are extinguished, so
// RECALL_TOGGLE can restore the same scene. Called while a.mu is held, and
// only when something is actually lit: turning off an already-dark room must
// not overwrite the memory with an empty set.
func (a *App) rememberPoolLocked() {
	if len(a.pool) == 0 {
		return
	}
	last := make(map[int]bool, len(a.pool))
	for n := range a.pool {
		last[n] = true
	}
	a.lastPool = last
}

// RecallToggle turns off whatever is lit, or — when the room is dark — brings
// back exactly the pads that were on last time, rather than every bound light.
// This is the Stream Deck status key's press action.
func (a *App) RecallToggle() HUDState {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("recall toggle recovered: %v", r)
		}
	}()
	a.mu.Lock()
	lit := len(a.pool) > 0
	want := make([]int, 0, len(a.lastPool))
	for n := range a.lastPool {
		want = append(want, n)
	}
	a.mu.Unlock()

	if lit {
		return a.AllOff()
	}
	if len(want) == 0 {
		// Nothing remembered — a first run, or the app restarted. Falling back
		// to every light is friendlier than a key that does nothing.
		return a.AllOn()
	}
	sort.Ints(want)
	for _, n := range want {
		// ToggleSlot is the only ignite path; guard it so a pad that somehow
		// came on in between is not flipped straight back off.
		a.mu.Lock()
		on := a.pool[n]
		a.mu.Unlock()
		if on {
			continue
		}
		if err := a.ToggleSlot(n); err != nil {
			log.Printf("recall pad %d: %v", n, err)
		}
	}
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

// ignitedBrightness is the value written to a lamp that is already on.
// Govee treats brightness 0 as power-off (LAN value 0 and BLE 0x33 0x04 0x00),
// so a fader at the bottom must still send 1%. Off is power packets only.
func ignitedBrightness(percent int) int {
	percent = clampBrightness(percent)
	if percent < 1 {
		return 1
	}
	return percent
}

// normalizeReportedBrightness maps a Govee status brightness onto 0–100.
// LAN firmware usually reports 1–100; some replies use the 0–255 BLE scale,
// and treating 235 as a percent would clamp the slider to 92 after a poll.
func normalizeReportedBrightness(v int) int {
	if v > 100 {
		return clampBrightness((v*100 + 127) / 255)
	}
	return clampBrightness(v)
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
			ui := clampBrightness(int(a.pendingBright.Load()))
			wire := ignitedBrightness(ui)
			a.mu.Lock()
			a.brightness = ui
			// Slider / MIDI CC counts as activity here (after coalesce), never
			// from the CoreMIDI callback, and never as a show/hide.
			a.lastActivity = time.Now()
			ips := a.poolIPsLocked()
			wasOff := a.lastSentBright == 0
			unchanged := a.lastSentBright == wire
			// Store the ignited value, never 0: lastSentBright==0 means AllOff,
			// not "user parked the fader at the bottom".
			a.lastSentBright = wire
			a.mu.Unlock()
			if len(ips) == 0 || unchanged {
				return
			}
			// Dim only. Never SendTurn(false) and never paint RGB 0,0,0 —
			// those are extinguish / palette paths, not fader motion.
			sendPoolBrightness(ips, wire, wasOff)
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
	dancing := a.dancing
	a.mu.Unlock()
	if dancing {
		return
	}
	a.paintScene(false)
}

// paintScene writes the current palette across the pool.
//
// Single mode: every lamp gets the same centre swatch.
// Gradient mode: complementary/adjacent swatches are spread across devices
// (that's the room-level scene). RGBIC strips and Lyra floor lamps (H6072)
// also get a zone ramp; classic bulbs such as H6001 take a solid colour.
//
// turnOn re-ignites each lamp first. Cycling the palette does that (the lamp
// may have been switched off at the wall).
func (a *App) paintScene(turnOn bool) {
	a.mu.Lock()
	dests := a.poolDestsLocked()
	grad := a.gradient
	pal := a.engine.Palette()
	swatches := a.engine.SceneColors(len(dests), grad)
	prev := map[string]color.RGBK{}
	for k, v := range a.lastColor {
		prev[k] = v
	}
	a.mu.Unlock()
	if len(dests) == 0 || len(swatches) == 0 {
		return
	}

	const steps = 4
	next := make([]color.RGBK, len(dests))
	for i, d := range dests {
		c := swatches[0]
		if i < len(swatches) {
			c = swatches[i]
		}
		// Gradient scenes must travel as RGB. Sending colorTemInKelvin>0 makes
		// Govee ignore RGB and light the white diodes — a pool of Warm Whites
		// then looks like one colour.
		if grad {
			c.Kelvin = 0
		}
		next[i] = c
		if turnOn {
			_ = govee.SendTurn(d.IP, true)
		}
		if grad && govee.SupportsSegments(d.Model) {
			_ = govee.SendGradient(d.IP, pal.Gradient(i, govee.GradientBands))
			continue
		}
		from, ok := prev[d.IP]
		if !ok || (from.R == c.R && from.G == c.G && from.B == c.B && from.Kelvin == c.Kelvin) {
			_ = govee.SendColor(d.IP, c.R, c.G, c.B, c.Kelvin)
			continue
		}
		for s := 1; s <= steps; s++ {
			mix := color.Lerp(from, c, float64(s)/float64(steps))
			k := mix.Kelvin
			if grad {
				k = 0
			}
			_ = govee.SendColor(d.IP, mix.R, mix.G, mix.B, k)
			if s < steps {
				time.Sleep(30 * time.Millisecond)
			}
		}
	}
	a.mu.Lock()
	if a.lastColor == nil {
		a.lastColor = map[string]color.RGBK{}
	}
	for i, d := range dests {
		a.lastColor[d.IP] = next[i]
	}
	a.mu.Unlock()
}

// ToggleGradient switches between a single colour for the whole pool and a
// complementary spread across devices, then repaints so the change is visible
// at once. Bound to `/` (slash / numpad divide). Persisted in config.json.
func (a *App) ToggleGradient() HUDState {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("gradient toggle recovered: %v", r)
		}
	}()
	a.recordUserActivity()
	a.mu.Lock()
	a.gradient = !a.gradient
	on := a.gradient
	dancing := a.dancing
	s := a.settings
	s.Gradient = on
	a.settings = s
	a.mu.Unlock()
	if err := config.SaveSettings(s); err != nil {
		log.Printf("persist gradient: %v", err)
	}

	// While dancing, the animation loop owns the colours and will pick the new
	// style up on its next tick; repainting here would only fight it.
	if !dancing {
		a.paintScene(true)
	}
	a.emitState()
	a.emit("gradient:toggle", on)
	return a.snapshot()
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
	a.mu.Unlock()
	a.paintScene(true)
	a.emitState()
	a.emit("color:cycle", pal.Name)
	return a.snapshot()
}

func (a *App) poolIPsLocked() []string {
	dests := a.poolDestsLocked()
	ips := make([]string, len(dests))
	for i, d := range dests {
		ips[i] = d.IP
	}
	return ips
}

type lampDest struct {
	IP    string
	Model string
}

func (a *App) poolDestsLocked() []lampDest {
	out := make([]lampDest, 0, 9)
	if a.pool == nil {
		return out
	}
	limit := len(a.slots)
	if limit > 9 {
		limit = 9
	}
	for n := 1; n <= limit; n++ {
		if !a.pool[n] {
			continue
		}
		ip, model, _ := a.slotLinkLocked(n)
		if ip == "" {
			continue
		}
		govee.Remember(ip, model)
		out = append(out, lampDest{IP: ip, Model: model})
	}
	return out
}

// slotLinkLocked returns the live control address for a pad. Catalog and BLE
// discovery can rewrite a stale CoreBluetooth UUID (H6001 C883 vs a dead
// identifier) without waiting for the user to re-save the map.
func (a *App) slotLinkLocked(n int) (ip, model, label string) {
	s := a.slots[n-1]
	ip = strings.TrimSpace(s.IP)
	model = s.Model
	label = s.Custom
	if label == "" {
		label = s.Name
	}
	for _, d := range a.catalog {
		if govee.NormalizeID(d.ID) == govee.NormalizeID(s.DeviceID) {
			if d.IP != "" {
				ip = d.IP
			}
			if model == "" && d.Model != "" {
				model = d.Model
			}
		}
		adv := d.AdvName
		if adv == "" {
			adv = d.Name
		}
		if d.IP != "" && govee.IsBLE(d.IP) && govee.BLEBindingMatch(d.Model, adv, s.Model, s.DeviceID, s.Name, s.Custom) {
			ip = d.IP
			if d.Model != "" {
				model = d.Model
			}
		}
	}
	return ip, model, label
}

func (a *App) OpenSetup() {
	a.OpenConfig()
}

func (a *App) OpenConfig() {
	a.mu.Lock()
	a.setupOpen = true
	a.discovering = true
	a.mu.Unlock()
	if a.ble != nil {
		a.ble.SetKeepScanning(true)
	}
	a.noteShow()
	a.sizeForMode(true)
	a.emitState()
	a.emit("hud:shown")
	go a.scanForConfig()
}

// scanForConfig keeps BLE discovery alive while the Lights tab is open.
// Govee's developer cloud often omits BLE-only bulbs (H6001); they only
// appear once CoreBluetooth hears them.
func (a *App) scanForConfig() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("config scan recovered: %v", r)
		}
	}()
	_ = a.udp.Scan()
	if a.ble != nil {
		a.ble.Scan()
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		a.mu.Lock()
		open := a.setupOpen
		a.mu.Unlock()
		if !open {
			break
		}
		if a.ble != nil {
			a.ble.Scan()
		}
		a.foldBLE()
		a.emitState()
	}
	a.mu.Lock()
	a.discovering = false
	a.mu.Unlock()
	a.emitState()
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
	a.mapDirty = true
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

// RenameSlot gives a pad a custom label. An empty name clears the rename and
// falls back to the discovered name. Persisted immediately: a rename is a
// deliberate edit, not part of the pad map the user still has to save.
func (a *App) RenameSlot(slot int, name string) (HUDState, error) {
	if slot < 1 || slot > 9 {
		return a.snapshot(), fmt.Errorf("slot must be 1-9")
	}
	name = strings.TrimSpace(name)
	if len(name) > 40 {
		name = name[:40]
	}
	a.mu.Lock()
	if slot > len(a.slots) || a.slots[slot-1].DeviceID == "" {
		st := a.snapshotLocked()
		a.mu.Unlock()
		return st, fmt.Errorf("pad %d has no light bound", slot)
	}
	a.slots[slot-1].Custom = name
	snapshot := config.SlotFile{Configured: a.configured, Slots: append([]config.SlotBinding(nil), a.slots...)}
	st := a.snapshotLocked()
	a.mu.Unlock()
	if err := config.SaveSlotFile(snapshot); err != nil {
		log.Printf("rename: save failed: %v", err)
	}
	a.emitState()
	return st, nil
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
	a.mapDirty = true
	st := a.snapshotLocked()
	a.mu.Unlock()
	a.emitState()
	return st, nil
}

func (a *App) CancelSetup() error {
	return a.CloseConfig()
}

// DiscardConfig leaves config without saving: in-memory pad edits (AssignSlot,
// MoveSlot) are thrown away by reloading the map from disk, so Escape cannot
// silently keep a binding the user chose not to save. Settings are not touched
// here -- the frontend simply drops its draft.
func (a *App) DiscardConfig() error {
	saved := config.LoadSlotFile()
	a.mu.Lock()
	if saved.Configured {
		a.slots = append([]config.SlotBinding(nil), saved.Slots...)
		a.configured = true
	}
	a.mapDirty = false
	// A pad whose binding just vanished must not stay lit in the pool.
	for n := range a.pool {
		if n < 1 || n > len(a.slots) || a.slots[n-1].DeviceID == "" {
			delete(a.pool, n)
		}
	}
	a.mu.Unlock()
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
	if a.ble != nil {
		a.ble.SetKeepScanning(false)
	}
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
	if err := a.SaveMappings(slots); err != nil {
		return err
	}
	a.mu.Lock()
	a.mapDirty = false
	a.mu.Unlock()
	return nil
}

// PersistNow writes the pad map and current config (gradient, MIDI, scene)
// without emptying the active pool. Bound to ⌘S. On the HUD there is no form
// to flush; this still snapshots the last scene so a relaunch comes back the same.
func (a *App) PersistNow() error {
	a.mu.Lock()
	snap := config.SlotFile{
		Configured: true,
		Slots:      append([]config.SlotBinding(nil), a.slots...),
	}
	a.configured = true
	s := a.settings
	s.Gradient = a.gradient
	a.settings = s
	a.mu.Unlock()
	if err := config.SaveSlotFile(snap); err != nil {
		return err
	}
	return config.SaveSettings(s)
}

// LiveCC reports the raw value the fader is currently sending, for the
// calibration UI. Returns -1 when no CC has arrived yet.
func (a *App) LiveCC() int {
	a.mu.Lock()
	l := a.midi
	a.mu.Unlock()
	if l == nil {
		return -1
	}
	_, val, ok := l.PeekRawCC()
	if !ok {
		return -1
	}
	return int(val)
}

// SaveCCCalibration stores the fader's measured endpoints and applies them
// immediately, so a full throw means 100% on this controller.
func (a *App) SaveCCCalibration(min, max int) error {
	if min < 0 || max > 127 || max <= min {
		return fmt.Errorf("bad calibration range %d-%d", min, max)
	}
	a.mu.Lock()
	s := a.settings
	s.MidiCCMin = min
	s.MidiCCMax = max
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

func (a *App) SaveSettings(in SettingsView) error {
	a.mu.Lock()
	s := a.settings
	s.MidiCC = in.MidiCC
	s.MidiCCAlt = in.MidiCCAlt
	s.MidiNotePlus = in.MidiNotePlus
	s.MidiNoteMinus = in.MidiNoteMinus
	s.MidiCCMin = in.MidiCCMin
	s.MidiCCMax = in.MidiCCMax
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
	wasHidden := a.hidden || a.hiding
	a.hidden = false
	a.hiding = false
	a.hideGen++
	a.mu.Unlock()
	a.sizeForMode(setup)
	// Restore opacity before the window is on screen, so it appears with the
	// UI already fully drawn instead of fading in after the fact.
	a.emit("hud:shown")
	a.emitState()
	runtime.WindowShow(ctx)
	// AlwaysOnTop is set once in main.go. Re-applying it on every show —
	// especially from the 300ms dock watcher — makes AppKit bounce a
	// frameless window. Only raise the app when coming back from a hide.
	if wasHidden {
		activateApp()
	}
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
	token := a.hideGen
	a.mu.Unlock()
	a.emit("hud:fade-out")
	go func() {
		// Just long enough for the quick CSS fade to land; anything slower
		// reads as the UI dismantling itself piece by piece.
		time.Sleep(90 * time.Millisecond)
		a.mu.Lock()
		// Abort only if ShowHUD / drag / config bumped hideGen. Clicks, keys,
		// and MIDI must not emit hud:shown here or the shell flickers.
		if a.hideGen != token || a.setupOpen || a.dragging {
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
	a.hideGen++
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
	hidden := a.hidden || a.hiding
	a.mu.Unlock()
	if hidden {
		a.ShowHUD()
		return
	}
	a.HideHUD()
}

// inactivityLoop no longer hides anything: the HUD stays up until the user
// dismisses it (Enter, Stream Deck toggle, or --toggle). It only clears a
// stale window-drag flag if a drag never received its pointerup. The stale
// threshold is 2s, so a 500ms tick resolves it just as well as the old 120ms
// one at a quarter of the wakeups.
func (a *App) inactivityLoop() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		a.mu.Lock()
		if a.dragging && time.Since(a.lastActivity) > 2*time.Second {
			a.dragging = false
		}
		a.mu.Unlock()
	}
}
