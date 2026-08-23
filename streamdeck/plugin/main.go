// Command lightwave-sd is the Stream Deck plugin for Lightwave.
//
// It is a protocol adapter, not a second copy of the app: Stream Deck events
// come in over a WebSocket, and lighting commands go out to the running
// Lightwave daemon over its Unix socket. All device logic — LAN discovery,
// the CoreBluetooth bridge, palettes, rate limiting — stays in Lightwave.
//
// That split is also what makes Bluetooth work at all. macOS gates
// CoreBluetooth on the *responsible* process, and the Stream Deck app declares
// no Bluetooth usage string, so a plugin touching CoreBluetooth directly would
// be denied. Lightwave.app holds that permission, so the plugin borrows it by
// asking Lightwave to do the work.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"lightwave-sd/internal/lw"
	"lightwave-sd/internal/sd"
)

// Action UUIDs, matching manifest.json.
const (
	actPad        = "com.dinksf.lightwave.pad"
	actAllOff     = "com.dinksf.lightwave.alloff"
	actPalette    = "com.dinksf.lightwave.palette"
	actDance      = "com.dinksf.lightwave.dance"
	actBrightness = "com.dinksf.lightwave.brightness"
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
		client:   lw.NewClient(),
		contexts: map[string]*instance{},
	}

	conn, err := sd.Dial(*port, *pluginUUID, *registerEvent)
	if err != nil {
		log.Fatalf("connect to Stream Deck: %v", err)
	}
	p.sd = conn

	// Lightwave state pushes drive key appearance, so keys reflect changes made
	// from the HUD, the numpad, or the lamps themselves.
	go p.client.Subscribe(p.onLightwaveState)

	conn.Run(p.onEvent)
}

func setupLogging() {
	dir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	logDir := filepath.Join(dir, "Library", "Logs", "Lightwave")
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

	mu       sync.Mutex
	contexts map[string]*instance
	state    lw.State
	haveState bool
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

func (p *plugin) trackContext(ev sd.Event) {
	var s settings
	if len(ev.Payload.Settings) > 0 {
		_ = json.Unmarshal(ev.Payload.Settings, &s)
	}
	p.mu.Lock()
	p.contexts[ev.Context] = &instance{action: ev.Action, context: ev.Context, settings: s}
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
	case actPalette:
		if inst.settings.Direction == "prev" {
			cmd = "PALETTE -1"
		} else {
			cmd = "PALETTE +1"
		}
	case actBrightness:
		step := inst.settings.Step
		if step == 0 {
			step = 10
		}
		if inst.settings.Mode == "down" {
			cmd = "BRIGHTNESS -" + strconv.Itoa(step)
		} else {
			cmd = "BRIGHTNESS +" + strconv.Itoa(step)
		}
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

// rotate handles a Stream Deck + dial, mapped to brightness.
func (p *plugin) rotate(ev sd.Event) {
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
		if pad == nil || !pad.Bound {
			p.sd.SetTitle(inst.context, "pad "+strconv.Itoa(inst.settings.Pad)+"\nunbound")
			p.sd.SetState(inst.context, 0)
			return
		}
		label := pad.Name
		if pad.Link == "" {
			label += "\n(no link)"
		}
		p.sd.SetTitle(inst.context, wrapTitle(label))
		if pad.On {
			p.sd.SetState(inst.context, 1)
		} else {
			p.sd.SetState(inst.context, 0)
		}
	case actBrightness:
		p.sd.SetTitle(inst.context, strconv.Itoa(st.Brightness)+"%")
	case actPalette:
		p.sd.SetTitle(inst.context, wrapTitle(st.Palette))
	case actDance:
		if st.Dancing {
			p.sd.SetState(inst.context, 1)
		} else {
			p.sd.SetState(inst.context, 0)
		}
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
