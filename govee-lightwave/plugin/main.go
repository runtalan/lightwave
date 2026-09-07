// Govee Lightwave is a self-contained Stream Deck plugin. It discovers and
// controls LAN-enabled Govee devices directly; it never contacts Lightwave.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const pluginID = "com.dinksf.govee-lightwave"
const (
	actPower       = pluginID + ".power"
	actBrightness  = pluginID + ".brightness"
	actColor       = pluginID + ".color"
	actTemperature = pluginID + ".temperature"
	actPalette     = pluginID + ".palette"
	actScene       = pluginID + ".scene"
	actFade        = pluginID + ".fade"
	actSweep       = pluginID + ".sweep"
	actStatus      = pluginID + ".status"
)

type event struct {
	Event, Action, Context string
	Payload                struct {
		Settings json.RawMessage `json:"settings"`
		Ticks    int             `json:"ticks"`
	} `json:"payload"`
}
type conn struct {
	c  net.Conn
	mu sync.Mutex
}

func dial(port int, uuid, reg string) (*conn, error) {
	c, e := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if e != nil {
		return nil, e
	}
	k := make([]byte, 16)
	_, _ = rand.Read(k)
	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", port, base64.StdEncoding.EncodeToString(k))
	if _, e = c.Write([]byte(req)); e != nil {
		c.Close()
		return nil, e
	}
	b := make([]byte, 0, 512)
	one := make([]byte, 1)
	for len(b) < 8192 {
		if _, e = c.Read(one); e != nil {
			c.Close()
			return nil, e
		}
		b = append(b, one[0])
		if len(b) >= 4 && string(b[len(b)-4:]) == "\r\n\r\n" {
			break
		}
	}
	if !strings.Contains(string(b), " 101 ") {
		c.Close()
		return nil, fmt.Errorf("WebSocket upgrade rejected")
	}
	w := &conn{c: c}
	_ = w.send(map[string]string{"event": reg, "uuid": uuid})
	return w, nil
}
func (w *conn) send(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return w.frame(1, b)
}
func (w *conn) frame(op byte, b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	h := []byte{0x80 | op}
	if n < 126 {
		h = append(h, 0x80|byte(n))
	} else if n <= 65535 {
		h = append(h, 0x80|126, 0, 0)
		binary.BigEndian.PutUint16(h[2:], uint16(n))
	} else {
		return fmt.Errorf("message too large")
	}
	m := make([]byte, 4)
	_, _ = rand.Read(m)
	h = append(h, m...)
	for i := range b {
		b[i] ^= m[i%4]
	}
	_, e := w.c.Write(append(h, b...))
	return e
}
func (w *conn) read() ([]byte, byte, error) {
	var h [2]byte
	if _, e := io.ReadFull(w.c, h[:]); e != nil {
		return nil, 0, e
	}
	n := uint64(h[1] & 127)
	if n == 126 {
		var x [2]byte
		if _, e := io.ReadFull(w.c, x[:]); e != nil {
			return nil, 0, e
		}
		n = uint64(binary.BigEndian.Uint16(x[:]))
	}
	if n > 1<<20 {
		return nil, 0, fmt.Errorf("frame too large")
	}
	var m [4]byte
	if h[1]&128 != 0 {
		if _, e := io.ReadFull(w.c, m[:]); e != nil {
			return nil, 0, e
		}
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(w.c, b); e != nil {
		return nil, 0, e
	}
	if h[1]&128 != 0 {
		for i := range b {
			b[i] ^= m[i%4]
		}
	}
	return b, h[0] & 15, nil
}
func (w *conn) run(f func(event)) {
	defer w.c.Close()
	for {
		b, op, e := w.read()
		if e != nil {
			return
		}
		if op == 9 {
			_ = w.frame(10, b)
			continue
		}
		if op != 1 {
			return
		}
		var x event
		if json.Unmarshal(b, &x) == nil {
			f(x)
		}
	}
}
func (w *conn) title(ctx, s string) {
	_ = w.send(map[string]any{"event": "setTitle", "context": ctx, "payload": map[string]any{"title": s, "target": 0}})
}
func (w *conn) state(ctx string, n int) {
	_ = w.send(map[string]any{"event": "setState", "context": ctx, "payload": map[string]int{"state": n}})
}
func (w *conn) alert(ctx string) { _ = w.send(map[string]string{"event": "showAlert", "context": ctx}) }
func (w *conn) pi(ctx, act string, p any) {
	_ = w.send(map[string]any{"event": "sendToPropertyInspector", "context": ctx, "payload": p})
}

type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	IP         string    `json:"ip"`
	On         bool      `json:"on"`
	Brightness int       `json:"brightness"`
	Seen       time.Time `json:"seen"`
}
type Room struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Devices []string `json:"devices"`
}
type Scene struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Power      *bool  `json:"power,omitempty"`
	Brightness *int   `json:"brightness,omitempty"`
	R          int    `json:"r,omitempty"`
	G          int    `json:"g,omitempty"`
	B          int    `json:"b,omitempty"`
}
type database struct {
	Devices map[string]Device `json:"devices"`
	Rooms   map[string]Room   `json:"rooms"`
	Scenes  map[string]Scene  `json:"scenes"`
}
type settings struct {
	Target  string `json:"target"`
	Mode    string `json:"mode"`
	Value   int    `json:"value"`
	R       int    `json:"r"`
	G       int    `json:"g"`
	B       int    `json:"b"`
	Palette string `json:"palette"`
	Scene   string `json:"scene"`
	Step    int    `json:"step"`
}
type app struct {
	sd       *conn
	mu       sync.Mutex
	db       database
	contexts map[string]struct {
		action string
		s      settings
	}
	udp   *net.UDPConn
	fades map[string]chan struct{}
}

func configPath() string {
	d, e := os.UserConfigDir()
	if e != nil {
		d = "."
	}
	return filepath.Join(d, "Govee Lightwave", "config.json")
}
func (a *app) load() {
	a.db = database{map[string]Device{}, map[string]Room{}, map[string]Scene{}}
	b, e := os.ReadFile(configPath())
	if e == nil {
		_ = json.Unmarshal(b, &a.db)
	}
	if a.db.Devices == nil {
		a.db.Devices = map[string]Device{}
	}
	if a.db.Rooms == nil {
		a.db.Rooms = map[string]Room{}
	}
	if a.db.Scenes == nil {
		a.db.Scenes = map[string]Scene{}
	}
}
func (a *app) save() {
	b, e := json.MarshalIndent(a.db, "", "  ")
	if e != nil {
		return
	}
	p := configPath()
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	_ = os.WriteFile(p+".tmp", b, 0600)
	_ = os.Rename(p+".tmp", p)
}
func normalize(s string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", "-", "", " ", "").Replace(s))
}
func (a *app) start() error {
	c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 4002})
	if e != nil {
		return e
	}
	a.udp = c
	go a.receive()
	return nil
}
func (a *app) receive() {
	b := make([]byte, 4096)
	for {
		n, from, e := a.udp.ReadFromUDP(b)
		if e != nil {
			return
		}
		var x struct {
			Msg struct {
				Cmd  string          `json:"cmd"`
				Data json.RawMessage `json:"data"`
			} `json:"msg"`
		}
		if json.Unmarshal(b[:n], &x) != nil {
			continue
		}
		switch x.Msg.Cmd {
		case "scan":
			var d struct{ Device, SKU, IP string }
			if json.Unmarshal(x.Msg.Data, &d) != nil {
				continue
			}
			id := normalize(d.Device)
			if id == "" {
				continue
			}
			a.mu.Lock()
			old := a.db.Devices[id]
			old.ID = id
			old.Model = d.SKU
			old.IP = d.IP
			if old.Name == "" {
				old.Name = d.SKU
			}
			old.Seen = time.Now()
			a.db.Devices[id] = old
			a.save()
			a.mu.Unlock()
		case "devStatus":
			var s struct {
				OnOff      int `json:"onOff"`
				Brightness int `json:"brightness"`
			}
			if json.Unmarshal(x.Msg.Data, &s) != nil {
				continue
			}
			a.mu.Lock()
			for id, d := range a.db.Devices {
				if d.IP == from.IP.String() {
					d.On = s.OnOff == 1
					d.Brightness = s.Brightness
					d.Seen = time.Now()
					a.db.Devices[id] = d
				}
			}
			a.mu.Unlock()
			a.refresh()
		}
	}
}
func (a *app) scan() {
	p := []byte(`{"msg":{"cmd":"scan","data":{"account_topic":"reserve"}}}`)
	targets := []*net.UDPAddr{{IP: net.IPv4(239, 255, 255, 250), Port: 4001}, {IP: net.IPv4bcast, Port: 4001}}
	for _, t := range targets {
		_, _ = a.udp.WriteToUDP(p, t)
	}
}
func (a *app) send(ip, cmd string) {
	if net.ParseIP(ip) == nil {
		return
	}
	c, e := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(ip), Port: 4003})
	if e == nil {
		_, _ = c.Write([]byte(cmd))
		_ = c.Close()
	}
}
func (a *app) members(target string) []Device {
	a.mu.Lock()
	defer a.mu.Unlock()
	if d, ok := a.db.Devices[target]; ok {
		return []Device{d}
	}
	if r, ok := a.db.Rooms[target]; ok {
		out := []Device{}
		for _, id := range r.Devices {
			if d, ok := a.db.Devices[id]; ok {
				out = append(out, d)
			}
		}
		return out
	}
	out := []Device{}
	for _, d := range a.db.Devices {
		out = append(out, d)
	}
	return out
}
func (a *app) command(ds []Device, kind string, v ...int) {
	for _, d := range ds {
		switch kind {
		case "power":
			on := v[0]
			a.send(d.IP, fmt.Sprintf(`{"msg":{"cmd":"turn","data":{"value":%d}}}`, on))
			d.On = on == 1
		case "brightness":
			n := v[0]
			if n < 1 {
				n = 1
			}
			if n > 100 {
				n = 100
			}
			a.send(d.IP, fmt.Sprintf(`{"msg":{"cmd":"brightness","data":{"value":%d}}}`, n))
			d.Brightness = n
		case "color":
			a.send(d.IP, fmt.Sprintf(`{"msg":{"cmd":"colorwc","data":{"color":{"r":%d,"g":%d,"b":%d},"colorTemInKelvin":0}}}`, v[0], v[1], v[2]))
		case "temperature":
			a.send(d.IP, fmt.Sprintf(`{"msg":{"cmd":"colorwc","data":{"color":{"r":0,"g":0,"b":0},"colorTemInKelvin":%d}}}`, v[0]))
		}
		a.mu.Lock()
		a.db.Devices[d.ID] = d
		a.mu.Unlock()
	}
	a.mu.Lock()
	a.save()
	a.mu.Unlock()
	a.refresh()
}

var palettes = map[string][][3]int{"Ocean": {{0, 182, 255}, {31, 92, 255}, {126, 48, 255}}, "Sunset": {{255, 78, 50}, {255, 135, 45}, {232, 37, 143}}, "Candlelight": {{255, 107, 36}, {255, 144, 58}, {255, 190, 104}}, "Sage": {{100, 148, 126}, {125, 158, 139}, {83, 119, 105}}}

func (a *app) apply(s settings) {
	ds := a.members(s.Target)
	switch s.Mode {
	case "fade":
		a.toggleFade(s.Target, s.Palette)
	case "on":
		a.command(ds, "power", 1)
	case "off":
		a.command(ds, "power", 0)
	case "brightness":
		a.command(ds, "brightness", s.Value)
	case "color":
		a.command(ds, "color", s.R, s.G, s.B)
	case "temperature":
		a.command(ds, "temperature", s.Value)
	case "palette":
		p := palettes[s.Palette]
		if len(p) == 0 {
			p = palettes["Ocean"]
		}
		for i, d := range ds {
			c := p[i%len(p)]
			a.command([]Device{d}, "color", c[0], c[1], c[2])
		}
	case "scene":
		a.mu.Lock()
		sc := a.db.Scenes[s.Scene]
		a.mu.Unlock()
		if sc.Power != nil {
			v := 0
			if *sc.Power {
				v = 1
			}
			a.command(ds, "power", v)
		}
		if sc.Brightness != nil {
			a.command(ds, "brightness", *sc.Brightness)
		}
		if sc.R+sc.G+sc.B > 0 {
			a.command(ds, "color", sc.R, sc.G, sc.B)
		}
	}
}

// toggleFade owns color changes for one target. It deliberately changes whole
// device colors only: no undocumented RGBIC segment packets are used here.
func (a *app) toggleFade(target, palette string) {
	a.mu.Lock()
	if stop := a.fades[target]; stop != nil {
		close(stop)
		delete(a.fades, target)
		a.mu.Unlock()
		a.refresh()
		return
	}
	stop := make(chan struct{})
	a.fades[target] = stop
	a.mu.Unlock()
	colors := palettes[palette]
	if len(colors) == 0 {
		colors = palettes["Ocean"]
	}
	go func() {
		tick := time.NewTicker(12 * time.Second)
		defer tick.Stop()
		index := 0
		for {
			ds := a.members(target)
			for i, d := range ds {
				c := colors[(index+i)%len(colors)]
				a.command([]Device{d}, "color", c[0], c[1], c[2])
			}
			index++
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()
}
func (a *app) refresh() {
	a.mu.Lock()
	cs := make(map[string]struct {
		action string
		s      settings
	}, len(a.contexts))
	for k, v := range a.contexts {
		cs[k] = v
	}
	a.mu.Unlock()
	for c, x := range cs {
		ds := a.members(x.s.Target)
		on, total := 0, len(ds)
		for _, d := range ds {
			if d.On {
				on++
			}
		}
		if total == 0 {
			a.sd.title(c, "CHOOSE\nLIGHTS")
			a.sd.state(c, 0)
			continue
		}
		name := fmt.Sprintf("%d/%d ON", on, total)
		switch x.action {
		case actPower:
			if x.s.Mode == "on" {
				name = "LIGHTS\nON"
			} else if x.s.Mode == "off" {
				name = "LIGHTS\nOFF"
			}
		case actBrightness:
			name = fmt.Sprintf("BRIGHT\n%d%%", x.s.Value)
		case actPalette:
			name = strings.ToUpper(x.s.Palette)
		case actStatus:
			name = fmt.Sprintf("%d/%d ON", on, total)
		}
		a.sd.title(c, name)
		if on > 0 {
			a.sd.state(c, 1)
		} else {
			a.sd.state(c, 0)
		}
	}
}
func (a *app) sendInspector(ctx, act string) {
	a.mu.Lock()
	ds := make([]Device, 0, len(a.db.Devices))
	for _, d := range a.db.Devices {
		ds = append(ds, d)
	}
	rs := make([]Room, 0, len(a.db.Rooms))
	for _, r := range a.db.Rooms {
		rs = append(rs, r)
	}
	a.mu.Unlock()
	sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
	a.sd.pi(ctx, act, map[string]any{"devices": ds, "rooms": rs, "palettes": []string{"Ocean", "Sunset", "Candlelight", "Sage"}})
}
func (a *app) handle(e event) {
	switch e.Event {
	case "willAppear", "didReceiveSettings":
		var s settings
		_ = json.Unmarshal(e.Payload.Settings, &s)
		// A key dragged from Stream Deck must be useful before its inspector has
		// saved a setting. The inspector will retain these defaults thereafter.
		if s.Mode == "" {
			switch e.Action {
			case actPower, actStatus:
				s.Mode = "on"
			case actBrightness:
				s.Mode, s.Value = "brightness", 70
			case actColor:
				s.Mode, s.R, s.G, s.B = "color", 126, 48, 255
			case actTemperature:
				s.Mode, s.Value = "temperature", 2700
			case actPalette:
				s.Mode, s.Palette = "palette", "Ocean"
			case actFade:
				s.Mode, s.Palette = "fade", "Ocean"
			case actScene:
				s.Mode = "scene"
			case actSweep:
				s.Mode, s.Value = "brightness", 70
			}
		}
		a.mu.Lock()
		a.contexts[e.Context] = struct {
			action string
			s      settings
		}{e.Action, s}
		a.mu.Unlock()
		a.refresh()
	case "willDisappear":
		a.mu.Lock()
		delete(a.contexts, e.Context)
		a.mu.Unlock()
	case "propertyInspectorDidAppear", "sendToPlugin":
		a.sendInspector(e.Context, e.Action)
	case "keyUp", "dialDown":
		a.mu.Lock()
		x, ok := a.contexts[e.Context]
		a.mu.Unlock()
		if !ok {
			return
		}
		if e.Action == actStatus {
			if anyOn(a.members(x.s.Target)) {
				x.s.Mode = "off"
			} else {
				x.s.Mode = "on"
			}
		}
		a.apply(x.s)
	case "dialRotate":
		a.mu.Lock()
		x, ok := a.contexts[e.Context]
		a.mu.Unlock()
		if !ok || e.Payload.Ticks == 0 {
			return
		}
		x.s.Mode = "brightness"
		step := x.s.Step
		if step == 0 {
			step = 2
		}
		x.s.Value += e.Payload.Ticks * step
		if x.s.Value < 1 {
			x.s.Value = 1
		}
		if x.s.Value > 100 {
			x.s.Value = 100
		}
		a.apply(x.s)
	}
}
func anyOn(ds []Device) bool {
	for _, d := range ds {
		if d.On {
			return true
		}
	}
	return false
}
func main() {
	port := flag.Int("port", 0, "")
	uuid := flag.String("pluginUUID", "", "")
	reg := flag.String("registerEvent", "registerPlugin", "")
	flag.Parse()
	if *port == 0 || *uuid == "" {
		log.Fatal("Stream Deck launch arguments missing")
	}
	w, e := dial(*port, *uuid, *reg)
	if e != nil {
		log.Fatal(e)
	}
	a := &app{sd: w, contexts: map[string]struct {
		action string
		s      settings
	}{}, fades: map[string]chan struct{}{}}
	a.load()
	if e = a.start(); e != nil {
		log.Printf("LAN listener unavailable: %v", e)
	} else {
		a.scan()
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				a.scan()
			}
		}()
	}
	w.run(a.handle)
}
