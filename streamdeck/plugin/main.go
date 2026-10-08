// Command lightwave-sd is the Stream Deck plugin for Lightwave.
//
// It is a protocol adapter, not a second copy of the app: Stream Deck events
// come in over a WebSocket, and lighting commands go out to the running
// Lightwave daemon over its local socket. All device logic — LAN discovery,
// the BLE bridge, palettes, rate limiting — stays in Lightwave.
//
// That split is also what makes Bluetooth work. The OS gates BLE on the
// responsible process, and the Stream Deck app does not hold that permission,
// so the plugin asks Lightwave to do the work.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"lightwave-sd/internal/lw"
	"lightwave-sd/internal/render"
	"lightwave-sd/internal/sd"
)

// Action UUIDs, matching manifest.json.
const (
	actPad        = "com.dinksf.lightwave.pad"
	actAllOff     = "com.dinksf.lightwave.alloff"
	actPalette    = "com.dinksf.lightwave.palette"
	actDance      = "com.dinksf.lightwave.dance"
	actGradient   = "com.dinksf.lightwave.gradient"
	actBrightness = "com.dinksf.lightwave.brightness"
	actStatus     = "com.dinksf.lightwave.status"
	actSweep      = "com.dinksf.lightwave.sweep"
	actMode       = "com.dinksf.lightwave.mode"
	actMonitor    = "com.dinksf.lightwave.monitor"
)

// Controller kinds Stream Deck reports for an action instance.
const encoder = "Encoder"

// Sweep pacing. One light per step is the whole point of the knob — the room
// should fill and empty visibly rather than all at once — and the gap also
// keeps a fast spin from firing eight lamp commands into the daemon at once.
const (
	sweepStepDelay = 300 * time.Millisecond
	// A knob left alone this long re-anchors to the lights as they actually
	// are, so a pad toggled from the HUD or the numpad in the meantime does not
	// make the next turn jump.
	sweepIdle = 2 * time.Second
)

func main() {
	port := flag.Int("port", 0, "Stream Deck websocket port")
	pluginUUID := flag.String("pluginUUID", "", "plugin instance uuid")
	registerEvent := flag.String("registerEvent", "registerPlugin", "registration event name")
	info := flag.String("info", "", "registration info json")
	flag.Parse()
	_ = info

	setupLogging()
	log.Printf("lightwave-sd starting (port=%d uuid=%s)", *port, *pluginUUID)

	if *port == 0 || *pluginUUID == "" {
		log.Fatalf("missing -port/-pluginUUID; this binary is launched by Stream Deck")
	}

	p := &plugin{
		client:    lw.NewClient(),
		contexts:  map[string]*instance{},
		sweepWake: make(chan struct{}, 1),
	}

	conn, err := sd.Dial(*port, *pluginUUID, *registerEvent)
	if err != nil {
		log.Fatalf("connect to Stream Deck: %v", err)
	}
	p.sd = conn

	// Lightwave state pushes drive key appearance, so keys reflect changes made
	// from the HUD, the numpad, or the lamps themselves.
	go p.client.Subscribe(p.onLightwaveState)
	go p.animate()
	go p.sweepLoop()

	conn.Run(p.onEvent)
}

func setupLogging() {
	var logDir string
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return
		}
		logDir = filepath.Join(base, "Lightwave", "Logs")
	} else {
		dir, err := os.UserHomeDir()
		if err != nil {
			return
		}
		logDir = filepath.Join(dir, "Library", "Logs", "Lightwave")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "streamdeck-plugin.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

// instance is one key on the deck.
type instance struct {
	action   string
	context  string
	settings settings
	// controller is "Keypad" or "Encoder". A dial draws into its touch-strip
	// layout rather than a key image, so the same action renders differently.
	controller string
	// offline is set while the key shows the "Lightwave offline" title, so the
	// first real state clears it.
	offline bool
	// custom is set once the user types their own key title in Stream Deck.
	// Long lamp names ("ClaudiaBulbH6001-C883") do not wrap onto a 72px key,
	// so the light name is only a starting point: the plugin seeds it, and
	// from the first manual edit onward it leaves the title alone.
	custom bool
	// seeded guards the one-time write of the light name, so a title the user
	// deliberately cleared is not re-filled on the next state push.
	seeded bool
	// knob is which page a monitor bar dial is on: knobBrightness,
	// knobScene, or "" for temperature. Pushing the dial moves to the next.
	// Held here rather than read back from settings on every event, because
	// an event already in flight still carries the settings from before the
	// push and would move it straight back.
	knob string
}

type settings struct {
	Pad       int    `json:"pad,omitempty"`
	Direction string `json:"direction,omitempty"`
	// Knob is a monitor bar dial's saved page; see instance.knob.
	Knob string `json:"knob,omitempty"`
}

// The monitor bar dial's pages, in the order a push walks them. Temperature
// is the empty string, so settings saved before the pages had names still
// read as it.
const (
	knobTemperature = ""
	knobBrightness  = "brightness"
	knobScene       = "scene"
)

var monitorKnobs = []string{knobTemperature, knobBrightness, knobScene}

// nextKnob is the page a push on the monitor bar dial moves to.
func nextKnob(knob string) string {
	for i, k := range monitorKnobs {
		if k == knob {
			return monitorKnobs[(i+1)%len(monitorKnobs)]
		}
	}
	return knobTemperature
}

type plugin struct {
	sd     *sd.Conn
	client *lw.Client

	mu        sync.Mutex
	contexts  map[string]*instance
	state     lw.State
	haveState bool
	phase     float64

	// How many lights the sweep knob is currently asking for, and when it was
	// last turned. sweepLoop walks the room towards the target one light at a
	// time; sweepWake nudges it awake.
	sweepTarget int
	sweepAt     time.Time
	sweepWake   chan struct{}
}

func (p *plugin) onEvent(ev sd.Event) {
	switch ev.Event {
	case "willAppear":
		p.trackContext(ev)
		p.refreshOne(ev.Context)
	case "willDisappear":
		p.mu.Lock()
		delete(p.contexts, ev.Context)
		p.mu.Unlock()
	case "didReceiveSettings":
		p.trackContext(ev)
		p.refreshOne(ev.Context)
	case "titleParametersDidChange":
		p.onTitleChanged(ev)
	case "propertyInspectorDidAppear", "sendToPlugin":
		// The inspector cannot reach Lightwave itself, so hand it the current
		// pads or palettes as soon as it opens (and again if it asks).
		p.sendInspector(ev)
	case "keyUp":
		p.trackContext(ev)
		p.press(ev)
	case "dialRotate":
		p.trackContext(ev)
		p.rotate(ev)
	case "dialDown", "touchTap":
		p.trackContext(ev)
		p.press(ev)
	}
}

// padOption is one entry in the Property Inspector's light dropdown.
type padOption struct {
	Pad   int    `json:"pad"`
	Name  string `json:"name"`
	Bound bool   `json:"bound"`
}

// sendInspector hands the Property Inspector what its dropdown needs from
// Lightwave: the bound lights for a pad key, the palette list for a palette key.
func (p *plugin) sendInspector(ev sd.Event) {
	if ev.Action != actPad && ev.Action != actPalette {
		return
	}
	p.mu.Lock()
	st, have := p.state, p.haveState
	p.mu.Unlock()
	if !have {
		if fresh, err := p.client.Command("STATE"); err == nil {
			p.applyState(fresh)
			st, have = fresh, true
		}
	}
	if ev.Action == actPalette {
		// Without Lightwave the inspector keeps its built-in list.
		if have && len(st.Palettes) > 0 {
			p.sd.SendToPropertyInspector(ev.Context, ev.Action, map[string]any{"palettes": st.Palettes})
		}
		return
	}
	p.sendPads(ev, st, have)
}

// sendPads hands the Property Inspector the nine pads with whatever Lightwave
// currently has bound to them, so the dropdown can offer real device names
// instead of "Pad 8". Falls back to the bare pad numbers when the daemon is not
// reachable — an inspector that lists nothing would be worse than one that
// lists numbers.
func (p *plugin) sendPads(ev sd.Event, st lw.State, have bool) {
	opts := make([]padOption, 0, 9)
	for n := 1; n <= 9; n++ {
		o := padOption{Pad: n, Name: "Pad " + strconv.Itoa(n)}
		if have {
			if pad := st.Pad(n); pad != nil && pad.Bound && strings.TrimSpace(pad.Name) != "" {
				o.Name, o.Bound = pad.Name, true
			}
		}
		opts = append(opts, o)
	}
	p.sd.SendToPropertyInspector(ev.Context, ev.Action, map[string]any{"pads": opts})
}

// onTitleChanged records whether the title on a pad key is the plugin's own
// seeded light name or something the user typed. Stream Deck sends this both
// when the plugin sets a title and when a person edits one, so the comparison
// against the light name is what distinguishes the two.
func (p *plugin) onTitleChanged(ev sd.Event) {
	p.mu.Lock()
	inst := p.contexts[ev.Context]
	if inst == nil || inst.action != actPad {
		p.mu.Unlock()
		return
	}
	seeded, st, have := inst.seeded, p.state, p.haveState
	p.mu.Unlock()
	if !seeded || !have {
		return
	}
	want := ""
	if pad := st.Pad(inst.settings.Pad); pad != nil {
		want = wrapTitle(pad.Name)
	}
	p.mu.Lock()
	// Anything other than the exact string the plugin wrote is the user's.
	inst.custom = ev.Payload.Title != want
	p.mu.Unlock()
}

func (p *plugin) trackContext(ev sd.Event) {
	var s settings
	if len(ev.Payload.Settings) > 0 {
		_ = json.Unmarshal(ev.Payload.Settings, &s)
	}
	// A fresh pad key has no saved pad, but its inspector already shows Pad 7
	// selected, so treat unset as 7 rather than leaving the key blank.
	if ev.Action == actPad && s.Pad == 0 {
		s.Pad = 7
	}
	p.mu.Lock()
	inst := p.contexts[ev.Context]
	if inst == nil {
		inst = &instance{context: ev.Context, knob: s.Knob}
		p.contexts[ev.Context] = inst
	}
	// Keep the title bookkeeping: only action and settings come from the event.
	inst.action, inst.settings = ev.Action, s
	// Only appearance and settings events name the controller; key and dial
	// events that omit it must not wipe it.
	if ev.Payload.Controller != "" {
		inst.controller = ev.Payload.Controller
	}
	p.mu.Unlock()
}

// press handles a key release, a dial push, and a tap on the touch strip.
func (p *plugin) press(ev sd.Event) {
	p.mu.Lock()
	inst := p.contexts[ev.Context]
	st, have := p.state, p.haveState
	p.mu.Unlock()
	if inst == nil {
		return
	}
	warm := have && st.WarmMode
	// Leaving Warmness mode returns to whichever pattern was last in use.
	backToColors := "MODE " + lw.ModeSolid
	if st.Gradient {
		backToColors = "MODE " + lw.ModePalette
	}

	var cmds []string
	switch inst.action {
	case actPad:
		if inst.settings.Pad < 1 || inst.settings.Pad > 9 {
			p.sd.ShowAlert(ev.Context)
			log.Printf("pad action on %s has no pad configured", ev.Context)
			return
		}
		cmds = []string{"TOGGLE_SLOT " + strconv.Itoa(inst.settings.Pad)}
	case actAllOff:
		cmds = []string{"ALL_TOGGLE"}
	case actDance:
		// The fade does not run on a white, so starting it from Warmness mode
		// goes back to the palette first rather than doing nothing.
		if warm {
			cmds = append(cmds, backToColors)
		}
		cmds = append(cmds, "DANCE")
	case actGradient:
		if warm {
			// The key reads "Use Colors" here: pattern means nothing on a white.
			cmds = []string{backToColors}
		} else {
			cmds = []string{"GRADIENT"}
		}
	case actMode:
		cmds = []string{"MODE next"}
	case actMonitor:
		// Pushing moves to the dial's next page. Nothing is sent to the lamp.
		p.mu.Lock()
		inst.knob = nextKnob(inst.knob)
		saved := settings{Knob: inst.knob}
		p.mu.Unlock()
		p.sd.SetSettings(ev.Context, saved)
		p.refreshOne(ev.Context)
		return
	case actPalette:
		if inst.controller == encoder {
			// The dial turns through palettes or temperatures; pushing it is
			// how one dial reaches all three modes.
			cmds = []string{"MODE next"}
			break
		}
		// "set:<name>" jumps straight to one palette; anything else keeps the
		// original prev/next cycling, so profiles saved before direct select
		// existed still work.
		if name, ok := strings.CutPrefix(inst.settings.Direction, "set:"); ok && name != "" {
			cmds = []string{"PALETTE SET " + name}
			if warm {
				// Picking a palette means wanting to see it. Set it first so
				// leaving Warmness mode paints the room once, in that palette.
				cmds = append(cmds, backToColors)
			}
		} else if inst.settings.Direction == "prev" {
			// In Warmness mode Lightwave reads these as cooler/warmer.
			cmds = []string{"PALETTE -1"}
		} else {
			cmds = []string{"PALETTE +1"}
		}
	case actSweep:
		// Pushing the knob is the shortcut past the slow walk: everything off,
		// or everything back on. The next turn re-anchors from there.
		cmds = []string{"ALL_TOGGLE"}
	case actStatus:
		// The status key is the "is anything on?" key, so pressing it answers
		// that: turn the room off, or bring back exactly the lights that were
		// on last time. Palette cycling lives on its own keys, which show
		// where they land — doing it from here was a hidden side effect.
		cmds = []string{"RECALL_TOGGLE"}
	case actBrightness:
		// Deliberately inert: brightness belongs to the app's slider, and this
		// key is only a readout. Dial rotation still adjusts it — see rotate() —
		// because a dial is a slider rather than a button.
		return
	default:
		return
	}
	p.run(ev.Context, cmds...)
}

// run sends commands in order, stopping at the first failure, and repaints
// from the last reply.
func (p *plugin) run(context string, cmds ...string) {
	for _, cmd := range cmds {
		st, err := p.client.Command(cmd)
		if err != nil {
			log.Printf("command %q: %v", cmd, err)
			p.sd.ShowAlert(context)
			return
		}
		p.applyState(st)
	}
}

// rotate handles a Stream Deck + dial.
func (p *plugin) rotate(ev sd.Event) {
	p.mu.Lock()
	inst := p.contexts[ev.Context]
	p.mu.Unlock()
	if inst != nil && inst.action == actSweep {
		p.rotateSweep(ev)
		return
	}
	if inst != nil && inst.action == actPalette {
		p.rotatePalette(ev)
		return
	}
	if inst != nil && inst.action == actMode {
		p.rotateMode(ev)
		return
	}
	if inst != nil && inst.action == actMonitor {
		p.rotateMonitor(ev, inst)
		return
	}
	p.rotateBrightness(ev)
}

// rotateMonitor turns the monitor bar's temperature, brightness or scene,
// whichever page the dial is on.
func (p *plugin) rotateMonitor(ev sd.Event, inst *instance) {
	p.mu.Lock()
	st, have, knob := p.state, p.haveState, inst.knob
	p.mu.Unlock()
	if !have {
		fresh, err := p.client.Command("STATE")
		if err != nil {
			log.Printf("monitor dial: %v", err)
			p.sd.ShowAlert(ev.Context)
			return
		}
		p.applyState(fresh)
		st = fresh
	}
	cmd, ok := monitorCmd(st, knob, ev.Payload.Ticks)
	if !ok {
		p.sd.ShowAlert(ev.Context)
		return
	}
	if cmd != "" {
		p.run(ev.Context, cmd)
	}
}

// monitorCmd is the command one turn of a monitor bar dial sends. ok is false
// when no bound lamp has a front light; an empty cmd means nothing to do.
//
// Clockwise is warmer, as on the palette dial, brighter, or the next scene.
// Brightness is the bar's trim: its share of the main slider, so the bar moves
// alone and the slider still carries it with the rest of the room. In Warmness
// mode the main warmth drives the whole bar, front light included, so the dial
// turns that. Scenes play on the back light; Lightwave holds the list and
// wraps it, with the room's own colours between the last scene and the first.
func monitorCmd(st lw.State, knob string, ticks int) (cmd string, ok bool) {
	pad := st.FrontPad()
	if pad == nil {
		return "", false
	}
	if ticks == 0 {
		return "", true
	}
	switch {
	case knob == knobScene:
		return "BAR_SCENE " + strconv.Itoa(ticks), true
	case knob == knobBrightness:
		trim := pad.Trim
		if trim < 1 || trim > 100 {
			trim = 100
		}
		return "TRIM " + strconv.Itoa(pad.Number) + " " + strconv.Itoa(clampInt(trim+ticks*2, 1, 100)), true
	case st.WarmMode:
		return "WARMNESS " + strconv.Itoa(st.Warmness-ticks*100), true
	}
	return "FRONT_WARMTH " + strconv.Itoa(st.FrontWarmth-ticks*100), true
}

// rotatePalette turns through palettes, or through temperatures in Warmness
// mode, one step per detent. Clockwise is the next palette, or warmer.
func (p *plugin) rotatePalette(ev sd.Event) {
	ticks := ev.Payload.Ticks
	if ticks == 0 {
		return
	}
	p.mu.Lock()
	st, have := p.state, p.haveState
	p.mu.Unlock()
	var cmd string
	switch {
	case have && st.WarmMode:
		cmd = "WARMNESS " + strconv.Itoa(st.Warmness-ticks*100)
	case have && st.PaletteIndex() >= 0:
		// Jump by the whole turn at once: a fast spin arrives as one event
		// with several ticks, and PALETTE +1 would only move one.
		n := len(st.Palettes)
		i := ((st.PaletteIndex()+ticks)%n + n) % n
		cmd = "PALETTE SET " + strconv.Itoa(i)
	case ticks < 0:
		cmd = "PALETTE -1"
	default:
		cmd = "PALETTE +1"
	}
	p.run(ev.Context, cmd)
}

// rotateMode steps through Warmness, Palette and Solid.
func (p *plugin) rotateMode(ev sd.Event) {
	ticks := ev.Payload.Ticks
	if ticks == 0 {
		return
	}
	p.mu.Lock()
	st, have := p.state, p.haveState
	p.mu.Unlock()
	switch {
	case have:
		p.run(ev.Context, "MODE "+st.StepMode(ticks))
	case ticks < 0:
		p.run(ev.Context, "MODE prev")
	default:
		p.run(ev.Context, "MODE next")
	}
}

// rotateBrightness maps a dial to the pool brightness.
func (p *plugin) rotateBrightness(ev sd.Event) {
	ticks := ev.Payload.Ticks
	if ticks == 0 {
		return
	}
	delta := ticks * 2 // a gentle ramp; a full turn is a large sweep
	sign := "+"
	if delta < 0 {
		sign = "-"
		delta = -delta
	}
	st, err := p.client.Command("BRIGHTNESS " + sign + strconv.Itoa(delta))
	if err != nil {
		log.Printf("dial brightness: %v", err)
		return
	}
	p.applyState(st)
}

// rotateSweep moves the light-count target the knob is aiming for. It does not
// switch anything itself: turning the knob is instant, but the lights are not,
// so the work is handed to sweepLoop and paced there.
func (p *plugin) rotateSweep(ev sd.Event) {
	ticks := ev.Payload.Ticks
	if ticks == 0 {
		return
	}
	p.mu.Lock()
	st, have := p.state, p.haveState
	p.mu.Unlock()
	if !have {
		fresh, err := p.client.Command("STATE")
		if err != nil {
			log.Printf("sweep: %v", err)
			p.sd.ShowAlert(ev.Context)
			return
		}
		p.applyState(fresh)
		st = fresh
	}
	on, total := st.CountOn()
	if total == 0 {
		// Nothing bound: there is no room to walk through.
		p.sd.ShowAlert(ev.Context)
		return
	}

	p.mu.Lock()
	if time.Since(p.sweepAt) > sweepIdle {
		p.sweepTarget = on
	}
	p.sweepTarget = clampInt(p.sweepTarget+ticks, 0, total)
	p.sweepAt = time.Now()
	p.mu.Unlock()

	select {
	case p.sweepWake <- struct{}{}:
	default: // already running or already asked to run
	}
}

// sweepLoop walks the lit lights towards the knob's target, one light and one
// step delay at a time. It re-reads the state on every step, so a target the
// knob moves mid-walk — or a pad switched somewhere else — is picked up on the
// next light rather than fought over.
func (p *plugin) sweepLoop() {
	for range p.sweepWake {
		for {
			p.mu.Lock()
			st, have, target := p.state, p.haveState, p.sweepTarget
			p.mu.Unlock()
			if !have {
				break
			}
			pad, on, done := sweepStep(st, target)
			if done {
				break
			}
			cmd := "SLOT_OFF "
			if on {
				cmd = "SLOT_ON "
			}
			next, err := p.client.Command(cmd + strconv.Itoa(pad))
			if err != nil {
				log.Printf("sweep %s%d: %v", cmd, pad, err)
				break
			}
			p.applyState(next)
			time.Sleep(sweepStepDelay)
		}
	}
}

// sweepStep names the one light to change to move the room towards target.
//
// The order is pad order: turning right lights the lowest dark pad, so the room
// fills 1, 2, 3, ...; turning left darkens the highest lit pad, so a left turn
// undoes a right turn light for light. Picking from the live state rather than
// replaying a remembered sequence is what lets the knob pick up wherever the
// lights happen to be.
func sweepStep(st lw.State, target int) (pad int, on, done bool) {
	lowestDark, highestLit, lit := 0, 0, 0
	for n := 1; n <= 9; n++ {
		p := st.Pad(n)
		if p == nil || !p.Bound {
			continue
		}
		if p.On {
			lit++
			highestLit = n
		} else if lowestDark == 0 {
			lowestDark = n
		}
	}
	switch {
	case lit < target && lowestDark != 0:
		return lowestDark, true, false
	case lit > target && highestLit != 0:
		return highestLit, false, false
	}
	return 0, false, true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// onLightwaveState receives pushes from the daemon.
func (p *plugin) onLightwaveState(st lw.State) {
	p.applyState(st)
}

func (p *plugin) applyState(st lw.State) {
	p.mu.Lock()
	p.state = st
	p.haveState = true
	ctxs := make([]*instance, 0, len(p.contexts))
	for _, inst := range p.contexts {
		ctxs = append(ctxs, inst)
	}
	p.mu.Unlock()
	for _, inst := range ctxs {
		p.render(inst, st)
	}
}

func (p *plugin) refreshOne(context string) {
	p.mu.Lock()
	inst := p.contexts[context]
	st, have := p.state, p.haveState
	p.mu.Unlock()
	if inst == nil {
		return
	}
	if !have {
		// No push yet — ask directly so a key painted at startup is correct.
		if fresh, err := p.client.Command("STATE"); err == nil {
			p.applyState(fresh)
			return
		}
		p.mu.Lock()
		inst.offline = true
		p.mu.Unlock()
		if inst.controller == encoder {
			p.sd.SetFeedback(context, map[string]any{"title": "Lightwave", "value": "Offline"})
			return
		}
		p.sd.SetTitle(context, "Lightwave\noffline")
		return
	}
	p.render(inst, st)
}

// render paints one key from the current Lightwave state.
func (p *plugin) render(inst *instance, st lw.State) {
	p.mu.Lock()
	wasOffline := inst.offline
	inst.offline = false
	dial := inst.controller == encoder
	p.mu.Unlock()
	if wasOffline && !dial {
		// An empty title hands the key back to whatever the user typed.
		p.sd.SetTitle(inst.context, "")
	}
	switch inst.action {
	case actPad:
		pad := st.Pad(inst.settings.Pad)
		p.mu.Lock()
		custom := inst.custom
		// Seed the light's name the first time this key is seen, then leave the
		// title alone so a shorter name typed in Stream Deck survives.
		seed := !inst.seeded && !custom
		if seed && pad != nil && pad.Bound {
			inst.seeded = true
		}
		p.mu.Unlock()
		if pad == nil || !pad.Bound {
			// Unbound is worth saying, but not at the cost of a title the user
			// typed — their label stays, and the dark key state carries it.
			if !custom {
				p.sd.SetTitle(inst.context, "pad "+strconv.Itoa(inst.settings.Pad)+"\nunbound")
			}
			p.sd.SetState(inst.context, 0)
			return
		}
		if seed {
			label := wrapTitle(pad.Name)
			if pad.Link == "" {
				// Appended after wrapping: wrapTitle re-flows on whitespace and
				// would otherwise swallow this deliberate line break.
				label += "\n(no link)"
			}
			p.sd.SetTitle(inst.context, label)
		}
		if pad.On {
			p.sd.SetState(inst.context, 1)
		} else {
			p.sd.SetState(inst.context, 0)
		}
	case actBrightness:
		// A read-only gauge: the slider owns the value, this reports it.
		if img, err := render.Level(st.Brightness, st.AnyOn()); err == nil {
			p.sd.SetImage(inst.context, img)
		} else {
			log.Printf("level render: %v", err)
		}
		// On a Stream Deck + dial the image fills only the icon slot; the
		// touch strip's value comes from the layout, as with the other dials.
		if dial {
			p.sd.SetFeedback(inst.context, map[string]any{
				"title":     "Brightness",
				"value":     strconv.Itoa(st.Brightness) + "%",
				"indicator": st.Brightness,
			})
		}
	case actSweep:
		// The dial reports how far through the room the knob has walked, as a
		// count and as the same arc gauge the brightness dial uses.
		on, total := st.CountOn()
		pct := 0
		if total > 0 {
			pct = on * 100 / total
		}
		if img, err := render.Level(pct, on > 0); err == nil {
			p.sd.SetImage(inst.context, img)
		} else {
			log.Printf("sweep render: %v", err)
		}
		p.sd.SetFeedback(inst.context, map[string]any{
			"title":     "Lights",
			"value":     strconv.Itoa(on) + "/" + strconv.Itoa(total),
			"indicator": pct,
		})
	case actPalette:
		if dial {
			p.renderPaletteDial(inst, st)
			break
		}
		p.renderPaletteKey(inst, st)
	case actMode:
		p.renderMode(inst, st, dial)
	case actMonitor:
		p.renderMonitor(inst, st)
	case actDance:
		// Title names the action, not the state: the key says what it will do.
		if st.Dancing {
			p.sd.SetState(inst.context, 1)
			p.sd.SetTitle(inst.context, "Stop\nFade")
		} else {
			p.sd.SetState(inst.context, 0)
			p.sd.SetTitle(inst.context, "Start\nFade")
		}
	case actGradient:
		if st.WarmMode {
			// Pattern only applies to colours; from a white the key goes back.
			p.sd.SetState(inst.context, 0)
			p.sd.SetTitle(inst.context, "Use\nColors")
		} else if st.Gradient {
			p.sd.SetState(inst.context, 1)
			p.sd.SetTitle(inst.context, "Use\nSolid")
		} else {
			p.sd.SetState(inst.context, 0)
			p.sd.SetTitle(inst.context, "Use\nGradient")
		}
	case actStatus:
		p.renderStatus(inst, st)
	case actAllOff:
		// Two states so the key shows whether anything is currently lit.
		if st.AnyOn() {
			p.sd.SetState(inst.context, 1)
		} else {
			p.sd.SetState(inst.context, 0)
		}
	}
}

// renderPaletteDial fills the palette dial's touch strip with what it is
// turning: the current palette and its colours, or the current temperature.
func (p *plugin) renderPaletteDial(inst *instance, st lw.State) {
	fb := map[string]any{"title": lw.ModeLabel(st.ColorMode())}
	var strip string
	var err error
	switch st.ColorMode() {
	case lw.ModeWarmness:
		fb["value"] = strconv.Itoa(st.Warmness) + "K · " + strconv.Itoa(warmthPct(st.Warmness)) + "% warm"
		strip, err = render.WarmStrip(st.Warmness)
	case lw.ModeSolid:
		fb["value"] = st.Palette
		if c, ok := st.LeadSwatch(); ok {
			strip, err = render.SolidStrip(swatches([]lw.Color{c})[0])
		}
	default:
		fb["value"] = st.Palette
		strip, err = render.SwatchStrip(swatches(st.Swatches))
	}
	if err != nil {
		log.Printf("palette strip render: %v", err)
	} else if strip != "" {
		fb["strip"] = strip
	}
	p.sd.SetFeedback(inst.context, fb)
}

// renderMonitor fills the monitor bar dial's touch strip with whichever of its
// pages is live, so a glance says what a turn will change.
func (p *plugin) renderMonitor(inst *instance, st lw.State) {
	p.mu.Lock()
	knob := inst.knob
	p.mu.Unlock()
	pad := st.FrontPad()
	if pad == nil {
		p.sd.SetFeedback(inst.context, map[string]any{"title": "Monitor Bar", "value": "No light bar", "strip": ""})
		return
	}
	name := "Monitor Bar"
	if !pad.On {
		name = "Bar off"
	}
	fb := map[string]any{}
	var strip string
	var err error
	switch knob {
	case knobScene:
		fb["title"] = name + " · Scene"
		switch {
		case st.BarScene != "":
			fb["value"] = st.BarScene
			strip, err = render.PositionStrip(st.BarSceneIndex, st.BarSceneCount)
		case st.BarSceneCount == 0:
			// An older Lightwave, or the scene list has not loaded yet.
			fb["value"] = "No scenes"
			strip, err = render.PositionStrip(0, 0)
		default:
			fb["value"] = "Room colors"
			strip, err = render.SwatchStrip(swatches(st.Swatches))
		}
	case knobBrightness:
		trim := pad.Trim
		if trim < 1 || trim > 100 {
			trim = 100
		}
		fb["title"] = name + " · Brightness"
		fb["value"] = strconv.Itoa(trim) + "% of main"
		strip, err = render.LevelStrip(trim)
	default:
		k := st.FrontWarmth
		if st.WarmMode {
			k = st.Warmness
		}
		k = clampInt(k, render.MinFrontKelvin, render.MaxFrontKelvin)
		warm := (render.MaxFrontKelvin - k) * 100 / (render.MaxFrontKelvin - render.MinFrontKelvin)
		fb["title"] = name + " · Temperature"
		fb["value"] = strconv.Itoa(k) + "K · " + strconv.Itoa(warm) + "% warm"
		strip, err = render.FrontStrip(k)
	}
	if err != nil {
		log.Printf("monitor strip render: %v", err)
	} else {
		fb["strip"] = strip
	}
	p.sd.SetFeedback(inst.context, fb)
}

// renderPaletteKey draws a palette key: where a press lands, or in Warmness
// mode the temperature it will set.
func (p *plugin) renderPaletteKey(inst *instance, st lw.State) {
	var img string
	var err error
	name, fixed := strings.CutPrefix(inst.settings.Direction, "set:")
	fixed = fixed && name != ""
	switch {
	case fixed:
		// A fixed jump target shows that palette itself, in any mode: pressing
		// it from Warmness mode goes straight to it.
		sw, known := st.PaletteSwatches[name]
		if !known {
			// Older Lightwave that does not send the full catalog: fall back
			// to the name rather than painting an empty key.
			p.sd.SetTitle(inst.context, wrapTitle(name))
			return
		}
		img, err = render.PaletteKey(name, swatches(sw))
	case st.WarmMode:
		// Next is warmer, previous cooler, matching Lightwave's own keys.
		warmer := inst.settings.Direction != "prev"
		k := st.Warmness + 100
		if warmer {
			k = st.Warmness - 100
		}
		img, err = render.WarmKey(clampInt(k, render.MinKelvin, render.MaxKelvin), warmer)
	default:
		// Show where a press lands, not the palette already showing.
		nav := render.Nav{Forward: inst.settings.Direction != "prev"}
		if nav.Forward {
			nav.Name, nav.Swatches = st.NextPalette, swatches(st.NextSwatches)
		} else {
			nav.Name, nav.Swatches = st.PrevPalette, swatches(st.PrevSwatches)
		}
		if nav.Name == "" {
			// Older Lightwave that does not send neighbours: fall back to the
			// current name rather than painting an empty key.
			p.sd.SetTitle(inst.context, wrapTitle(st.Palette))
			return
		}
		img, err = render.NavKey(nav)
	}
	if err != nil {
		log.Printf("palette render: %v", err)
		return
	}
	p.sd.SetImage(inst.context, img)
	// The image carries the text; clear any fallback title left from before.
	p.sd.SetTitle(inst.context, "")
}

// renderMode draws the mode switch on a key or a dial.
func (p *plugin) renderMode(inst *instance, st lw.State, dial bool) {
	mode := st.ColorMode()
	sw := swatches(st.Swatches)
	if dial {
		fb := map[string]any{"title": "Color Mode", "value": lw.ModeLabel(mode)}
		if strip, err := render.ModeStrip(mode, sw, st.Warmness); err == nil {
			fb["strip"] = strip
		} else {
			log.Printf("mode strip render: %v", err)
		}
		p.sd.SetFeedback(inst.context, fb)
		return
	}
	img, err := render.ModeKey(mode, st.StepMode(1), sw, st.Warmness)
	if err != nil {
		log.Printf("mode render: %v", err)
		return
	}
	p.sd.SetImage(inst.context, img)
}

// warmthPct expresses a temperature as how warm it is across Lightwave's range:
// 0% at the coolest white, 100% at the warmest.
func warmthPct(k int) int {
	return clampInt((render.MaxKelvin-k)*100/(render.MaxKelvin-render.MinKelvin), 0, 100)
}

// wrapTitle keeps long lamp names readable on a 72px key.
func wrapTitle(s string) string {
	if len(s) <= 9 {
		return s
	}
	words := strings.Fields(s)
	if len(words) < 2 {
		return s
	}
	var lines []string
	cur := ""
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= 9:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > 3 {
		lines = lines[:3]
	}
	return strings.Join(lines, "\n")
}

// renderStatus draws the live indicator key: palette name and colours, fade
// state, and how many lights are lit.
func (p *plugin) renderStatus(inst *instance, st lw.State) {
	on, total := st.CountOn()
	name, sw, grad := st.Palette, swatches(st.Swatches), st.Gradient
	if st.WarmMode {
		// A white has no palette: show the temperature and its colour.
		name, sw, grad = strconv.Itoa(st.Warmness)+"K", []render.Swatch{render.KelvinRGB(st.Warmness)}, false
	}
	p.mu.Lock()
	phase := p.phase
	p.mu.Unlock()
	img, err := render.Indicator(render.Status{
		Palette:    name,
		Swatches:   sw,
		Dancing:    st.Dancing,
		Gradient:   grad,
		Brightness: st.Brightness,
		OnNames:    st.OnNames(),
		LightsOn:   on,
		Total:      total,
		Phase:      phase,
	})
	if err != nil {
		log.Printf("indicator render: %v", err)
		return
	}
	p.sd.SetImage(inst.context, img)
}

// animate advances the indicator's wave. It only redraws while a status key is
// actually on screen, and runs slowly when the fade animation is off, so an
// idle deck is not repainted for no reason.
func (p *plugin) animate() {
	const step = 220 * time.Millisecond
	t := time.NewTicker(step)
	defer t.Stop()
	for range t.C {
		p.mu.Lock()
		var keys []*instance
		for _, inst := range p.contexts {
			if inst.action == actStatus {
				keys = append(keys, inst)
			}
		}
		if len(keys) == 0 {
			p.mu.Unlock()
			continue
		}
		st, have := p.state, p.haveState
		// A drifting wave when idle, faster while the colour fade runs, so the
		// key's motion mirrors what the lights are doing.
		inc := 0.012
		if st.Dancing {
			inc = 0.05
		}
		p.phase += inc
		if p.phase > 1 {
			p.phase -= 1
		}
		p.mu.Unlock()
		if !have {
			continue
		}
		for _, inst := range keys {
			p.renderStatus(inst, st)
		}
	}
}

// swatches converts wire colours into the renderer's form.
func swatches(cs []lw.Color) []render.Swatch {
	out := make([]render.Swatch, 0, len(cs))
	for _, c := range cs {
		out = append(out, render.Swatch{R: c.R, G: c.G, B: c.B})
	}
	return out
}
