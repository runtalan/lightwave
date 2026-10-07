// Package lw talks to the running Lightwave daemon over its Unix socket.
package lw

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	dialTimeout = 800 * time.Millisecond
	ioTimeout   = 3 * time.Second
	retryDelay  = 3 * time.Second
)

func sockPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "lightwave.sock")
	}
	return "/tmp/lightwave.sock"
}

// Pad mirrors one numpad slot.
type Pad struct {
	Number int    `json:"n"`
	Name   string `json:"name"`
	Bound  bool   `json:"bound"`
	On     bool   `json:"on"`
	Link   string `json:"link"`
	// Front marks a lamp with a white front light of its own, such as a
	// monitor light bar. Trim is its share of the brightness slider, 1-100;
	// older Lightwave builds send neither.
	Front bool `json:"front"`
	Trim  int  `json:"trim"`
}

// State is the snapshot Lightwave pushes to subscribers.
type State struct {
	Pads       []Pad  `json:"pads"`
	Brightness int    `json:"brightness"`
	Palette    string `json:"palette"`
	Dancing    bool   `json:"dancing"`
	Gradient   bool   `json:"gradient"`
	WarmMode   bool   `json:"warmMode"`
	Warmness   int    `json:"warmness"`
	// FrontWarmth is the temperature of front lights, in Kelvin.
	FrontWarmth int `json:"frontWarmth"`
	// BarScene is the Govee scene on the light bar's back light, "" while it
	// follows the room. BarSceneIndex is its place among BarSceneCount
	// scenes, counting from 1.
	BarScene      string  `json:"barScene"`
	BarSceneIndex int     `json:"barSceneIndex"`
	BarSceneCount int     `json:"barSceneCount"`
	Swatches      []Color `json:"swatches"`
	// Every palette name in cycle order, so a dial can jump several palettes
	// per turn and the Property Inspector can list them.
	Palettes []string `json:"palettes"`
	// Mode is "warmness", "palette" or "solid". Older Lightwave builds do not
	// send it; ColorMode derives it from the flags instead.
	Mode string `json:"mode"`
	// Palettes either side of the current one, so the next/previous keys can
	// show their destination rather than the palette already in play.
	PrevPalette  string  `json:"prevPalette"`
	NextPalette  string  `json:"nextPalette"`
	PrevSwatches []Color `json:"prevSwatches"`
	NextSwatches []Color `json:"nextSwatches"`
	// Every palette's colours, keyed by name, for jump-straight-to-palette
	// keys that target a palette other than the current/prev/next one.
	PaletteSwatches map[string][]Color `json:"paletteSwatches"`
}

// Color is one palette swatch.
type Color struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// Colour modes, in the order a mode switch steps through them.
const (
	ModeWarmness = "warmness"
	ModePalette  = "palette"
	ModeSolid    = "solid"
)

var Modes = []string{ModeWarmness, ModePalette, ModeSolid}

// ColorMode reports the current colour mode.
func (s State) ColorMode() string {
	switch {
	case s.Mode != "":
		return s.Mode
	case s.WarmMode:
		return ModeWarmness
	case s.Gradient:
		return ModePalette
	}
	return ModeSolid
}

// StepMode returns the mode n steps away from the current one, wrapping.
func (s State) StepMode(n int) string {
	cur := s.ColorMode()
	i := 0
	for j, m := range Modes {
		if m == cur {
			i = j
		}
	}
	k := len(Modes)
	return Modes[((i+n)%k+k)%k]
}

// ModeLabel is how a mode is named on a key or the touch strip.
func ModeLabel(m string) string {
	switch m {
	case ModeWarmness:
		return "Warmness"
	case ModePalette:
		return "Palette"
	case ModeSolid:
		return "Solid Color"
	}
	return m
}

// LeadSwatch is the one colour every light shares in solid mode: the palette's
// centre swatch, as Lightwave picks it.
func (s State) LeadSwatch() (Color, bool) {
	if len(s.Swatches) == 0 {
		return Color{}, false
	}
	return s.Swatches[len(s.Swatches)/2], true
}

// PaletteIndex returns the current palette's position in Palettes, or -1.
func (s State) PaletteIndex() int {
	for i, name := range s.Palettes {
		if name == s.Palette {
			return i
		}
	}
	return -1
}

// CountOn returns how many lights are lit and how many are bound.
func (s State) CountOn() (on, total int) {
	for i := range s.Pads {
		if !s.Pads[i].Bound {
			continue
		}
		total++
		if s.Pads[i].On {
			on++
		}
	}
	return on, total
}

// OnNames returns the names of the lit lights, in pad order. A controller can
// then name a single lit lamp instead of just counting it.
func (s State) OnNames() []string {
	var out []string
	for i := range s.Pads {
		if s.Pads[i].Bound && s.Pads[i].On {
			out = append(out, s.Pads[i].Name)
		}
	}
	return out
}

// FrontPad returns the first bound pad whose lamp has a front light, or nil.
func (s State) FrontPad() *Pad {
	for i := range s.Pads {
		if s.Pads[i].Bound && s.Pads[i].Front {
			return &s.Pads[i]
		}
	}
	return nil
}

// AnyOn reports whether at least one light is currently lit.
func (s State) AnyOn() bool {
	for i := range s.Pads {
		if s.Pads[i].On {
			return true
		}
	}
	return false
}

// Pad returns the pad with this number, or nil.
func (s State) Pad(n int) *Pad {
	for i := range s.Pads {
		if s.Pads[i].Number == n {
			return &s.Pads[i]
		}
	}
	return nil
}

type Client struct {
	mu sync.Mutex
}

func NewClient() *Client { return &Client{} }

// Command sends one command and parses the state reply. Each command uses its
// own short-lived connection: Lightwave answers and closes, and this keeps a
// stalled command from blocking the subscription.
func (c *Client) Command(cmd string) (State, error) {
	var st State
	conn, err := net.DialTimeout("unix", sockPath(), dialTimeout)
	if err != nil {
		return st, fmt.Errorf("lightwave not running: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return st, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return st, err
	}
	return parseState(line)
}

// Subscribe holds a connection open and calls onState for every push,
// reconnecting for as long as the plugin runs. Lightwave may be restarted or
// not yet started, so a failed connection is normal and simply retried.
func (c *Client) Subscribe(onState func(State)) {
	warned := false
	for {
		if err := c.subscribeOnce(onState); err != nil {
			if !warned {
				log.Printf("lightwave subscribe: %v (retrying every %s)", err, retryDelay)
				warned = true
			}
		} else {
			warned = false
		}
		time.Sleep(retryDelay)
	}
}

func (c *Client) subscribeOnce(onState func(State)) error {
	conn, err := net.DialTimeout("unix", sockPath(), dialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("SUBSCRIBE\n")); err != nil {
		return err
	}
	log.Printf("subscribed to lightwave state")
	r := bufio.NewReader(conn)
	for {
		// No read deadline: pushes are event-driven and may be far apart.
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		st, err := parseState(line)
		if err != nil {
			continue
		}
		onState(st)
	}
}

func parseState(line string) (State, error) {
	var st State
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "ERR ") {
		return st, fmt.Errorf("%s", strings.TrimPrefix(line, "ERR "))
	}
	if !strings.HasPrefix(line, "STATE ") {
		return st, fmt.Errorf("unexpected reply %q", line)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "STATE ")), &st); err != nil {
		return st, err
	}
	return st, nil
}
