package main

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
)

// RemoteCommand executes a command sent over the IPC socket by an external
// controller (the Stream Deck plugin) and returns a reply line. An empty reply
// means "not a remote command" — the caller then falls back to the original
// window-toggle handling, so existing --toggle/--setup behaviour is untouched.
//
// The wire format is deliberately plain text, one command per line:
//
//	STATE              -> JSON snapshot
//	TOGGLE_SLOT <1-9>  -> ignite/extinguish one pad
//	SLOT_ON <1-9>      -> ignite (idempotent)
//	SLOT_OFF <1-9>     -> extinguish (idempotent)
//	BRIGHTNESS <0-100> -> absolute level for the pool
//	BRIGHTNESS +/-<n>  -> relative nudge
//	ALL_OFF            -> everything off, pool cleared
//	ALL_ON             -> every bound light on at the slider level
//	ALL_TOGGLE         -> all off if anything is lit, else all on
//	DANCE              -> toggle the colour animation
//	GRADIENT           -> toggle single-colour vs gradient scenes
//	PALETTE <+1|-1>    -> cycle palettes; the reply also names the palettes
//	                      either side, so a key can show where it will land
//	PING               -> liveness probe
func (a *App) RemoteCommand(cmd string) string {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("remote: %q recovered: %v", cmd, r)
		}
	}()

	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	verb := strings.ToUpper(fields[0])
	arg := ""
	if len(fields) > 1 {
		arg = fields[1]
	}

	switch verb {
	case "PING":
		return "OK lightwave"

	case "STATE":
		return a.remoteState()

	case "TOGGLE_SLOT", "SLOT_ON", "SLOT_OFF":
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 9 {
			return "ERR pad must be 1-9"
		}
		if verb != "TOGGLE_SLOT" {
			// Idempotent variants: only act when the pad is not already in the
			// requested state, so repeated presses do not flip it back.
			want := verb == "SLOT_ON"
			a.mu.Lock()
			lit := a.pool[n]
			a.mu.Unlock()
			if lit == want {
				return a.remoteState()
			}
		}
		if err := a.ToggleSlot(n); err != nil {
			return "ERR " + err.Error()
		}
		return a.remoteState()

	case "BRIGHTNESS":
		if arg == "" {
			return "ERR brightness needs a value"
		}
		var target int
		if arg[0] == '+' || arg[0] == '-' {
			delta, err := strconv.Atoi(arg)
			if err != nil {
				return "ERR bad brightness delta"
			}
			a.mu.Lock()
			target = a.brightness + delta
			a.mu.Unlock()
		} else {
			v, err := strconv.Atoi(arg)
			if err != nil {
				return "ERR bad brightness"
			}
			target = v
		}
		a.SetBrightness(clampBrightness(target))
		return a.remoteState()

	case "ALL_OFF":
		a.AllOff()
		return a.remoteState()

	case "ALL_ON":
		a.AllOn()
		return a.remoteState()

	case "ALL_TOGGLE":
		a.ToggleAll()
		return a.remoteState()

	case "DANCE":
		a.ToggleDance()
		return a.remoteState()

	case "GRADIENT":
		a.ToggleGradient()
		return a.remoteState()

	case "PALETTE":
		// Still cycles both ways: PALETTE -1 is a published wire verb the Stream
		// Deck plugin binds to its own key. HUD +/− now match this.
		dir := 1
		if strings.HasPrefix(arg, "-") {
			dir = -1
		}
		a.CycleColor(dir)
		return a.remoteState()

	case "SHOW", "SETUP", "CONFIG", "TOGGLE", "SUBSCRIBE":
		// Window commands stay on the legacy fire-and-forget path.
		return ""
	}
	return ""
}

// RemoteState is the compact snapshot pushed to subscribers. It is deliberately
// smaller than HUDState: a key controller needs pad labels and on/off, not the
// whole device catalog.
type RemoteState struct {
	Pads       []RemotePad   `json:"pads"`
	Brightness int           `json:"brightness"`
	Palette    string        `json:"palette"`
	Dancing    bool          `json:"dancing"`
	Gradient   bool          `json:"gradient"`
	Swatches   []RemoteColor `json:"swatches"`
	// The palettes one step either side of the current one. A controller with
	// a next/previous key can then label each with where it will land instead
	// of repeating the name of the palette already showing.
	PrevPalette  string        `json:"prevPalette"`
	NextPalette  string        `json:"nextPalette"`
	PrevSwatches []RemoteColor `json:"prevSwatches"`
	NextSwatches []RemoteColor `json:"nextSwatches"`
}

// RemoteColor is one palette colour, so a controller can draw the palette
// rather than just name it.
type RemoteColor struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

type RemotePad struct {
	Number int    `json:"n"`
	Name   string `json:"name"`
	Bound  bool   `json:"bound"`
	On     bool   `json:"on"`
	Link   string `json:"link"` // "lan", "ble", or "" when unreachable
}

func (a *App) remoteState() string {
	a.mu.Lock()
	pal := a.engine.Palette()
	prevPal, nextPal := a.engine.Peek(-1), a.engine.Peek(1)
	st := RemoteState{
		Pads:       make([]RemotePad, 0, 9),
		Brightness: a.brightness,
		Palette:    pal.Name,
		Dancing:    a.dancing,
		Gradient:   a.gradient,
		Swatches:   make([]RemoteColor, 0, len(pal.Colors)),
	}
	for _, c := range pal.Colors {
		st.Swatches = append(st.Swatches, RemoteColor{R: c.R, G: c.G, B: c.B})
	}
	st.PrevPalette, st.NextPalette = prevPal.Name, nextPal.Name
	for _, c := range prevPal.Colors {
		st.PrevSwatches = append(st.PrevSwatches, RemoteColor{R: c.R, G: c.G, B: c.B})
	}
	for _, c := range nextPal.Colors {
		st.NextSwatches = append(st.NextSwatches, RemoteColor{R: c.R, G: c.G, B: c.B})
	}
	for i := 1; i <= 9 && i <= len(a.slots); i++ {
		s := a.slots[i-1]
		link := ""
		switch {
		case strings.HasPrefix(s.IP, "ble:"):
			link = "ble"
		case strings.TrimSpace(s.IP) != "":
			link = "lan"
		}
		st.Pads = append(st.Pads, RemotePad{
			Number: i,
			Name:   s.Label(),
			Bound:  s.DeviceID != "",
			On:     a.pool[i],
			Link:   link,
		})
	}
	a.mu.Unlock()

	b, err := json.Marshal(st)
	if err != nil {
		return "ERR " + err.Error()
	}
	return "STATE " + string(b)
}

// publishRemoteState pushes the current snapshot to subscribed controllers so
// Stream Deck keys track changes made from the HUD, the numpad, or the lamps
// themselves.
//
// Deduplicated: emitState fires on every status poll, but a controller only
// needs to hear about it when something actually changed. Without this a
// Stream Deck redraws its keys several times a second forever.
func (a *App) publishRemoteState() {
	srv := a.ipcServer()
	if srv == nil {
		return
	}
	line := a.remoteState()
	a.lastRemoteMu.Lock()
	same := line == a.lastRemote
	if !same {
		a.lastRemote = line
	}
	a.lastRemoteMu.Unlock()
	if same {
		return
	}
	srv.Publish(line)
}
