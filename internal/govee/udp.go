package govee

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	ScanPort    = 4001
	ListenPort  = 4002
	ControlPort = 4003
)

var multicastIP = net.IPv4(239, 255, 255, 250)

type lanMsg struct {
	Msg struct {
		Cmd  string          `json:"cmd"`
		Data json.RawMessage `json:"data"`
	} `json:"msg"`
}

type scanData struct {
	IP     string `json:"ip"`
	Device string `json:"device"`
	SKU    string `json:"sku"`
}

// DevStatus is a device's reply to a devStatus query.
type DevStatus struct {
	IP         string
	On         bool
	Brightness int
}

type devStatusData struct {
	OnOff      int `json:"onOff"`
	Brightness int `json:"brightness"`
}

type UDP struct {
	mu       sync.Mutex
	listen   *net.UDPConn
	seen     map[string]Device
	onDev    func(Device)
	onStat   func(DevStatus)
	statuses map[string]DevStatus
}

// OnStatus registers a callback for devStatus replies. Safe to call before Start.
func (u *UDP) OnStatus(f func(DevStatus)) {
	u.mu.Lock()
	u.onStat = f
	u.mu.Unlock()
}

// Status returns the last known status for an IP, if one has been received.
func (u *UDP) Status(ip string) (DevStatus, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s, ok := u.statuses[ip]
	return s, ok
}

func NewUDP() *UDP {
	return &UDP{seen: map[string]Device{}, statuses: map[string]DevStatus{}}
}

func (u *UDP) Start(onDevice func(Device)) error {
	u.onDev = onDevice
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: ListenPort}
	c, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("govee udp listen :%d: %w", ListenPort, err)
	}
	u.listen = c
	u.joinMulticast(c)
	go u.readLoop()
	return nil
}

// joinMulticast subscribes the listen socket to the Govee discovery group on
// every multicast-capable interface. Without an explicit IP_ADD_MEMBERSHIP the
// kernel drops group traffic, so devices that answer a scan by multicast are
// never seen. Devices that answer by unicast still arrive without this, which
// is why the omission can look like "some lights are simply LAN dark".
func (u *UDP) joinMulticast(c *net.UDPConn) {
	p := ipv4.NewPacketConn(c)
	group := &net.UDPAddr{IP: multicastIP}
	ifaces, err := net.Interfaces()
	if err != nil {
		if err := p.JoinGroup(nil, group); err != nil {
			log.Printf("govee: multicast join (default iface): %v", err)
		}
		return
	}
	joined := 0
	for i := range ifaces {
		ifi := ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if err := p.JoinGroup(&ifi, group); err != nil {
			continue
		}
		joined++
	}
	if joined == 0 {
		if err := p.JoinGroup(nil, group); err != nil {
			log.Printf("govee: multicast join failed on all interfaces: %v", err)
			return
		}
		joined = 1
	}
	log.Printf("govee: joined %s on %d interface(s)", multicastIP, joined)
}

func (u *UDP) Close() {
	if u.listen != nil {
		_ = u.listen.Close()
	}
	ctrlMu.Lock()
	conn := ctrlConn
	ctrlConn = nil
	ctrlClosed = true
	ctrlMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (u *UDP) readLoop() {
	if u.listen == nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		n, addr, err := u.listen.ReadFromUDP(buf)
		if err != nil {
			return
		}
		src := ""
		if addr != nil {
			src = addr.IP.String()
		}
		func(raw []byte, from string) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("govee: scan parse recovered: %v", r)
				}
			}()
			u.handle(raw, from)
		}(append([]byte(nil), buf[:n]...), src)
	}
}

func (u *UDP) handle(raw []byte, from string) {
	var msg lanMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	if msg.Msg.Cmd == "devStatus" {
		var st devStatusData
		if err := json.Unmarshal(msg.Msg.Data, &st); err != nil || from == "" {
			return
		}
		s := DevStatus{IP: from, On: st.OnOff == 1, Brightness: st.Brightness}
		u.mu.Lock()
		u.statuses[from] = s
		cb := u.onStat
		u.mu.Unlock()
		if cb != nil {
			cb(s)
		}
		return
	}
	if msg.Msg.Cmd != "scan" {
		return
	}
	var data scanData
	if err := json.Unmarshal(msg.Msg.Data, &data); err != nil {
		return
	}
	// A LAN scan reply carries no friendly name — only the SKU. Leaving Name
	// empty keeps the cloud-provided name (e.g. "Bedroom Floor Lamp") from
	// being overwritten with a model number by Merge.
	d := Device{
		ID:     NormalizeID(data.Device),
		Model:  data.SKU,
		IP:     data.IP,
		Online: data.IP != "",
	}
	if d.ID == "" {
		return
	}
	u.mu.Lock()
	u.seen[d.ID] = d
	u.mu.Unlock()
	if u.onDev != nil {
		u.onDev(d)
	}
}

// SeenIP reports whether any device has answered on this IP this run — a LAN
// scan reply or a devStatus. An IP that has never answered is no evidence of
// a working LAN path (some models hold a WiFi address yet have no LAN API).
func (u *UDP) SeenIP(ip string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.statuses[ip]; ok {
		return true
	}
	for _, d := range u.seen {
		if d.IP == ip {
			return true
		}
	}
	return false
}

func (u *UDP) Devices() []Device {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]Device, 0, len(u.seen))
	for _, d := range u.seen {
		out = append(out, d)
	}
	return out
}

func (u *UDP) Scan() error {
	payload := []byte(`{"msg":{"cmd":"scan","data":{"account_topic":"reserve"}}}`)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))

	targets := []*net.UDPAddr{
		{IP: multicastIP, Port: ScanPort},
		{IP: net.IPv4bcast, Port: ScanPort},
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok || ipnet.IP.To4() == nil {
					continue
				}
				bcast := broadcast(ipnet)
				if bcast != nil {
					targets = append(targets, &net.UDPAddr{IP: bcast, Port: ScanPort})
				}
			}
		}
	}

	var last error
	for _, t := range targets {
		if _, err := conn.WriteToUDP(payload, t); err != nil {
			last = err
		}
	}
	return last
}

func broadcast(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	mask := n.Mask
	if ip == nil || len(mask) != 4 {
		return nil
	}
	out := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		out[i] = ip[i] | ^mask[i]
	}
	return out
}

// QueryStatus asks a device for its current power/brightness. The reply
// arrives asynchronously on the :4002 listener.
func QueryStatus(ip string) error {
	if IsBLE(ip) {
		// BLE state readback needs notification plumbing; until then the HUD
		// trusts what it last sent to these lamps.
		return nil
	}
	return sendControl(ip, `{"msg":{"cmd":"devStatus","data":{}}}`)
}

func SendTurn(ip string, on bool) error {
	if IsBLE(ip) {
		return bleSend(ip, blePacketPower(on))
	}
	v := 0
	if on {
		v = 1
	}
	return sendControl(ip, fmt.Sprintf(`{"msg":{"cmd":"turn","data":{"value":%d}}}`, v))
}

// SendBrightness sends a Govee LAN brightness command.
// Official LAN payloads are integers 1–100. A request at or below 0 is sent as
// 1 — the dimmest the protocol allows — rather than switching the lamp off:
// the slider is a dimmer, and reaching the bottom of its travel should not
// power the light down.
func SendBrightness(ip string, percent int) error {
	if strings.TrimSpace(ip) == "" {
		return nil
	}
	if percent < 1 {
		percent = 1
	}
	if percent > 100 {
		percent = 100
	}
	if IsBLE(ip) {
		return bleSend(ip, blePacketBrightness(percent))
	}
	return sendControl(ip, fmt.Sprintf(`{"msg":{"cmd":"brightness","data":{"value":%d}}}`, percent))
}

// ApplyPoolBrightness dims every device that has a LAN IP. Empty IPs are skipped.
func ApplyPoolBrightness(ips []string, percent int, turnOn bool) {
	for _, ip := range ips {
		if strings.TrimSpace(ip) == "" {
			continue
		}
		func(ip string) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("govee: brightness send recovered (%s): %v", ip, r)
				}
			}()
			if turnOn {
				_ = SendTurn(ip, true)
			}
			_ = SendBrightness(ip, percent)
		}(ip)
	}
}

func SendColor(ip string, r, g, b, kelvin int) error {
	if IsBLE(ip) {
		// Classic single-zone lamps answer manual-color mode 0x02; RGBIC
		// models only answer the segment command and ignore 0x02. Firmware
		// drops the variant it does not speak, so send both rather than
		// maintaining a per-model table.
		err := bleSend(ip, blePacketColorLegacy(r, g, b))
		if err2 := bleSend(ip, blePacketColorSegment(r, g, b)); err == nil {
			err = err2
		}
		return err
	}
	if kelvin > 0 {
		return sendControl(ip, fmt.Sprintf(
			`{"msg":{"cmd":"colorwc","data":{"color":{"r":%d,"g":%d,"b":%d},"colorTemInKelvin":%d}}}`,
			r, g, b, kelvin,
		))
	}
	if err := sendControl(ip, fmt.Sprintf(
		`{"msg":{"cmd":"colorwc","data":{"color":{"r":%d,"g":%d,"b":%d},"colorTemInKelvin":0}}}`,
		r, g, b,
	)); err == nil {
		return nil
	}
	return sendControl(ip, fmt.Sprintf(`{"msg":{"cmd":"color","data":{"r":%d,"g":%d,"b":%d}}}`, r, g, b))
}

var (
	ctrlMu     sync.Mutex
	ctrlConn   *net.UDPConn
	ctrlClosed bool
)

// errShuttingDown reports a send issued after Close. Slider traffic is
// fire-and-forget, so callers ignore it rather than reopening a socket that
// shutdown just tore down.
var errShuttingDown = fmt.Errorf("govee: control socket closed")

func controlConn() (*net.UDPConn, error) {
	ctrlMu.Lock()
	defer ctrlMu.Unlock()
	if ctrlClosed {
		return nil, errShuttingDown
	}
	if ctrlConn != nil {
		return ctrlConn, nil
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	ctrlConn = c
	return c, nil
}

func sendControl(ip, payload string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("udp send panic: %v", r)
		}
	}()
	ip = strings.TrimSpace(ip)
	if ip == "" || IsBLE(ip) {
		return nil
	}
	raddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(ip, fmt.Sprintf("%d", ControlPort)))
	if err != nil {
		return err
	}
	conn, err := controlConn()
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(400 * time.Millisecond))
	_, err = conn.WriteToUDP([]byte(payload), raddr)
	return err
}
