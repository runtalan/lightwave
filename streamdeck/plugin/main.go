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
)

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
	// custom is set once the user types their own key title in Stream Deck.
	// Long lamp names ("ClaudiaBulbH6001-C883") do not wrap onto a 72px key,
	// so the light name is only a starting point: the plugin seeds it, and
	// from the first manual edit onward it leaves the title alone.
	custom bool
	// seeded guards the one-time write of the light name, so a title the user
	// deliberately cleared is not re-filled on the next state push.
	seeded bool
}

type settings struct {
	Pad       int    `json:"pad,omitempty"`
	Step      int    `json:"step,omitempty"`
	Direction string `json:"direction,omitempty"`
	Mode      string `json:"mode,omitempty"`
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
		// pads as soon as it opens (and again if it asks).
		p.sendPads(ev)
	case "keyUp":
		p.trackContext(ev)
		p.press(ev)
	case "dialRotate":
		p.trackContext(ev)
		p.rotate(ev)
	case "dialDown":
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

// sendPads hands the Property Inspector the nine pads with whatever Lightwave
// currently has bound to them, so the dropdown can offer real device names
// instead of "Pad 8". Falls back to the bare pad numbers when the daemon is not
// reachable — an inspector that lists nothing would be worse than one that
// lists numbers.
func (p *plugin) sendPads(ev sd.Event) {
	if ev.Action != actPad {
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
	p.mu.Lock()
	inst := p.contexts[ev.Context]
	if inst == nil {
		inst = &instance{context: ev.Context}
		p.contexts[ev.Context] = inst
	}
	// Keep the title bookkeeping: only action and settings come from the event.
	inst.action, inst.settings = ev.Action, s
	p.mu.Unlock()
}

// press handles a key release (and dial push).
func (p *plugin) press(ev sd.Event) {
	p.mu.Lock()
	inst := p.contexts[ev.Context]
	p.mu.Unlock()
	if inst == nil {
		return
	}

	var cmd string
	switch inst.action {
	case actPad:
		if inst.settings.Pad < 1 || inst.settings.Pad > 9 {
			p.sd.ShowAlert(ev.Context)
			log.Printf("pad action on %s has no pad configured", ev.Context)
			return
		}
		cmd = "TOGGLE_SLOT " + strconv.Itoa(inst.settings.Pad)
	case actAllOff:
		cmd = "ALL_TOGGLE"
	case actDance:
		cmd = "DANCE"
	case actGradient:
		cmd = "GRADIENT"
	case actPalette:
		p.mu.Lock()
		warm := p.haveState && p.state.WarmMode
		p.mu.Unlock()
		if warm {
			if inst.settings.Direction == "prev" {
				cmd = "PALETTE -1"
			} else {
				cmd = "PALETTE +1"
			}
			break
		}
		// "set:<name>" jumps straight to one palette; anything else keeps the
		// original prev/next cycling, so profiles saved before direct select
		// existed still work.
		if name, ok := strings.CutPrefix(inst.settings.Direction, "set:"); ok && name != "" {
			cmd = "PALETTE SET " + name
		} else if inst.settings.Direction == "prev" {
			cmd = "PALETTE -1"
		} else {
			cmd = "PALETTE +1"
		}
	case actSweep:
		// Pushing the knob is the shortcut past the slow walk: everything off,
		// or everything back on. The next turn re-anchors from there.
		cmd = "ALL_TOGGLE"
	case actStatus:
		// The status key is the "is anything on?" key, so pressing it answers
		// that: turn the room off, or bring back exactly the lights that were
		// on last time. Palette cycling lives on its own keys, which show
		// where they land — doing it from here was a hidden side effect.
		cmd = "RECALL_TOGGLE"
	case actBrightness:
		// Deliberately inert: brightness belongs to the app's slider, and this
		// key is only a readout. Dial rotation still adjusts it — see rotate() —
		// because a dial is a slider rather than a button.
		return
	default:
		return
	}

	st, err := p.client.Command(cmd)
	if err != nil {
		log.Printf("command %q: %v", cmd, err)
		p.sd.ShowAlert(ev.Context)
		return
	}
	p.applyState(st)
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
	p.rotateBrightness(ev)
}

// rotatePalette sends the published palette command. Lightwave turns that
// into warmer/cooler 100K steps whenever Warmness mode is active.
func (p *plugin) rotatePalette(ev sd.Event) {
	if ev.Payload.Ticks == 0 {
		return
	}
	dir := "+1"
	if ev.Payload.Ticks < 0 {
		dir = "-1"
	}
	st, err := p.client.Command("PALETTE " + dir)
	if err != nil {
		log.Printf("dial palette/warmness: %v", err)
		return
	}
	p.applyState(st)
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
		p.sd.SetTitle(context, "Lightwave\noffline")
		return
	}
	p.render(inst, st)
}

// render paints one key from the current Lightwave state.
func (p *plugin) render(inst *instance, st lw.State) {
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
			"title": "Lights",
			"value": strconv.Itoa(on) + "/" + strconv.Itoa(total),
		})
	case actPalette:
		if st.WarmMode {
			warmth := (6500 - st.Warmness) * 100 / 4500
			p.sd.SetTitle(inst.context, "Warmth\n"+strconv.Itoa(warmth)+"%\n"+strconv.Itoa(st.Warmness)+"K")
			p.sd.SetFeedback(inst.context, map[string]any{"title": "Warmth", "value": strconv.Itoa(warmth) + "%"})
			break
		}
		if name, ok := strings.CutPrefix(inst.settings.Direction, "set:"); ok && name != "" {
			// A fixed jump target: show that palette itself, not a direction.
			sw, known := st.PaletteSwatches[name]
			if !known {
				// Older Lightwave that does not send the full catalog: fall
				// back to the name rather than painting an empty key.
				p.sd.SetTitle(inst.context, wrapTitle(name))
				break
			}
			if img, err := render.PaletteKey(name, swatches(sw)); err == nil {
				p.sd.SetImage(inst.context, img)
			} else {
				log.Printf("palette render: %v", err)
			}
			break
		}
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
			break
		}
		if img, err := render.NavKey(nav); err == nil {
			p.sd.SetImage(inst.context, img)
		} else {
			log.Printf("nav render: %v", err)
		}
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
		if st.Gradient {
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
	sw := make([]render.Swatch, 0, len(st.Swatches))
	for _, c := range st.Swatches {
		sw = append(sw, render.Swatch{R: c.R, G: c.G, B: c.B})
	}
	p.mu.Lock()
	phase := p.phase
	p.mu.Unlock()
	img, err := render.Indicator(render.Status{
		Palette:    st.Palette,
		Swatches:   sw,
		Dancing:    st.Dancing,
		Gradient:   st.Gradient,
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
