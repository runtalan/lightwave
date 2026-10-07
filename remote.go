package main

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"lightwave/internal/color"
	"lightwave/internal/govee"
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
//	RECALL_TOGGLE      -> all off if anything is lit, else re-light exactly the
//	                      pads that were on last time
//	DANCE              -> toggle the colour animation
//	GRADIENT           -> toggle single-colour vs gradient scenes
//	PALETTE <+1|-1>    -> cycle palettes; the reply also names the palettes
//	                      either side, so a key can show where it will land
//	PALETTE SET <name|index> -> jump straight to one palette
//	WARM_MODE          -> switch between palette and warmness mode
//	MODE <warmness|palette|solid|next|prev>
//	                   -> switch colour mode in one step; palette and solid
//	                      are LightWave mode with the gradient on or off
//	WARMNESS <kelvin>  -> set a white temperature from 2000 to 6500K
//	FRONT_WARMTH <kelvin> -> set the front-light temperature, 2700 to 6500K,
//	                      on lamps that have a front light
//	TRIM <1-9> <0-100> -> one pad's share of the brightness slider; 0 clears it
//	BAR_SCENE <steps>|OFF|<name>
//	                   -> step through the Govee scenes on a front-light
//	                      lamp's back light, turn them off so it follows the
//	                      room again, or play one by name
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

	case "RECALL_TOGGLE":
		a.RecallToggle()
		return a.remoteState()

	case "DANCE":
		a.ToggleDance()
		return a.remoteState()

	case "GRADIENT":
		a.ToggleGradient()
		return a.remoteState()

	case "WARM_MODE":
		a.ToggleWarmMode()
		return a.remoteState()

	case "MODE":
		switch m := strings.ToLower(arg); m {
		case ModeWarmness, ModePalette, ModeSolid:
			a.SetColorMode(m)
		case "next", "+1":
			a.StepColorMode(1)
		case "prev", "-1":
			a.StepColorMode(-1)
		default:
			return "ERR mode must be warmness, palette, solid, next or prev"
		}
		return a.remoteState()

	case "WARMNESS":
		k, err := strconv.Atoi(arg)
		if err != nil {
			return "ERR warmness needs a Kelvin value"
		}
		a.SetWarmness(k)
		return a.remoteState()

	case "FRONT_WARMTH":
		k, err := strconv.Atoi(arg)
		if err != nil {
			return "ERR front warmth needs a Kelvin value"
		}
		a.SetFrontWarmth(k)
		return a.remoteState()

	case "TRIM":
		if len(fields) < 3 {
			return "ERR TRIM needs a pad and a percent"
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 9 {
			return "ERR pad must be 1-9"
		}
		pct, err := strconv.Atoi(fields[2])
		if err != nil {
			return "ERR bad trim"
		}
		if _, err := a.SetSlotTrim(n, pct); err != nil {
			return "ERR " + err.Error()
		}
		return a.remoteState()

	case "BAR_SCENE":
		rest := strings.TrimSpace(strings.Join(fields[1:], " "))
		if strings.EqualFold(rest, "OFF") {
			a.SetBarScene("")
			return a.remoteState()
		}
		var err error
		if n, nerr := strconv.Atoi(rest); nerr == nil {
			_, err = a.StepBarScene(n)
		} else {
			_, err = a.SelectBarScene(rest)
		}
		if err != nil {
			return "ERR " + err.Error()
		}
		return a.remoteState()

	case "PALETTE":
		if strings.EqualFold(arg, "SET") {
			// Direct select, by index or by name. Names win over indexes in
			// saved controller settings because they survive palette insertions;
			// the rest of the line is the name so spaces need no quoting.
			rest := strings.Join(fields[2:], " ")
			if rest == "" {
				return "ERR PALETTE SET needs a palette name or index"
			}
			idx, err := strconv.Atoi(rest)
			if err != nil {
				var ok bool
				if idx, ok = color.IndexOf(rest); !ok {
					return "ERR unknown palette " + strconv.Quote(rest)
				}
			}
			a.SetPalette(idx)
			return a.remoteState()
		}
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
	Pads       []RemotePad `json:"pads"`
	Brightness int         `json:"brightness"`
	Palette    string      `json:"palette"`
	Palettes   []string    `json:"palettes"`
	Dancing    bool        `json:"dancing"`
	Gradient   bool        `json:"gradient"`
	WarmMode   bool        `json:"warmMode"`
	Warmness   int         `json:"warmness"`
	// FrontWarmth is the temperature of front lights; see RemotePad.Front.
	FrontWarmth int `json:"frontWarmth"`
	// BarScene is the Govee scene on front-light lamps' back light, "" while
	// it follows the room. BarSceneIndex is its place in BAR_SCENE's ring,
	// 0 being the room, out of BarSceneCount scenes.
	BarScene      string `json:"barScene"`
	BarSceneIndex int    `json:"barSceneIndex"`
	BarSceneCount int    `json:"barSceneCount"`
	// Mode is "warmness", "palette" or "solid": the two flags above folded
	// into the one choice a mode switch presents.
	Mode     string        `json:"mode"`
	Swatches []RemoteColor `json:"swatches"`
	// Every palette's colours, keyed by name, so a controller with a
	// jump-straight-to-palette key can draw the palette it targets rather
	// than only the one currently playing or its immediate neighbours.
	PaletteSwatches map[string][]RemoteColor `json:"paletteSwatches"`
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
	// Front marks a lamp with a white front light of its own, the kind
	// FRONT_WARMTH drives.
	Front bool `json:"front,omitempty"`
	// Trim is this pad's share of the brightness slider, 1-100.
	Trim int `json:"trim"`
}

func (a *App) remoteState() string {
	a.mu.Lock()
	pal := a.engine.Palette()
	prevPal, nextPal := a.engine.Peek(-1), a.engine.Peek(1)
	st := RemoteState{
		Pads:            make([]RemotePad, 0, 9),
		Brightness:      a.brightness,
		Palette:         pal.Name,
		Palettes:        paletteNames,
		Dancing:         a.dancing,
		Gradient:        a.gradient,
		WarmMode:        a.warmMode,
		Warmness:        a.warmness,
		FrontWarmth:     clampFrontWarmth(a.frontWarmth),
		Mode:            a.colorModeLocked(),
		BarScene:        a.barScene,
		Swatches:        make([]RemoteColor, 0, len(pal.Colors)),
		PaletteSwatches: make(map[string][]RemoteColor, len(color.Palettes)),
	}
	scenes := a.sceneLib[a.frontModelLocked()]
	st.BarSceneCount = len(scenes)
	for i, sc := range scenes {
		if sc.Name == a.barScene {
			st.BarSceneIndex = i + 1
			break
		}
	}
	for _, p := range color.Palettes {
		cs := make([]RemoteColor, 0, len(p.Colors))
		for _, c := range p.Colors {
			cs = append(cs, RemoteColor{R: c.R, G: c.G, B: c.B})
		}
		st.PaletteSwatches[p.Name] = cs
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
			Front:  govee.HasFrontLight(s.Model),
			Trim:   s.EffectiveTrim(),
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
