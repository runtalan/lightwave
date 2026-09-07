// Package discovery finds devices without changing their lighting state.
package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

var group = net.IPv4(239, 255, 255, 250)
var scanPayload = []byte(`{"msg":{"cmd":"scan","data":{"account_topic":"reserve"}}}`)

type LANDevice struct{ ID, Model, IP string }
type LAN struct {
	mu       sync.Mutex
	listener *net.UDPConn
	joined   map[int]bool
	closed   bool
	port     int
	onDevice func(LANDevice)
	onStatus func(string, bool, int)
}

func NewLAN(found func(LANDevice), status func(string, bool, int)) *LAN {
	return &LAN{port: 4002, onDevice: found, onStatus: status}
}

// Scan retries binding after a port conflict. Never take over another app's
// exclusive socket: socket reuse would make unicast delivery nondeterministic.
func (l *LAN) Scan() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return net.ErrClosed
	}
	if l.listener == nil {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: l.port})
		if err != nil {
			return fmt.Errorf("Cannot receive LAN replies on UDP %d. Close other Govee LAN controllers and retry: %w", l.port, err)
		}
		l.listener = c
		l.joined = map[int]bool{}
		go l.receive(c)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	receiver := ipv4.NewPacketConn(l.listener)
	sent := 0
	var failures []error
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		if iface.Flags&net.FlagMulticast != 0 && !l.joined[iface.Index] {
			if err := receiver.JoinGroup(&iface, &net.UDPAddr{IP: group}); err == nil {
				l.joined[iface.Index] = true
			} else {
				failures = append(failures, err)
			}
		}
		for _, addr := range addrs {
			n, ok := addr.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() {
				continue
			}
			// Bind an ephemeral sender on each LAN, as Govee replies to 4002.
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: n.IP})
			if err != nil {
				failures = append(failures, err)
				continue
			}
			p := ipv4.NewPacketConn(c)
			_ = p.SetMulticastInterface(&iface)
			_ = p.SetMulticastTTL(1)
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			targets := []net.IP{group}
			if iface.Flags&net.FlagBroadcast != 0 {
				if broadcast := broadcastIP(n); broadcast != nil {
					targets = append(targets, broadcast)
				}
			}
			for _, ip := range targets {
				if _, err := c.WriteToUDP(scanPayload, &net.UDPAddr{IP: ip, Port: 4001}); err == nil {
					sent++
				} else {
					failures = append(failures, err)
				}
			}
			_ = c.Close()
		}
	}
	if sent == 0 {
		return fmt.Errorf("No LAN discovery requests sent. Check Wi-Fi/Ethernet and local-network permission: %v", errors.Join(failures...))
	}
	if len(l.joined) == 0 {
		return fmt.Errorf("LAN scan sent, but multicast replies cannot be received: %v", errors.Join(failures...))
	}
	return nil
}

func broadcastIP(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	ones, bits := n.Mask.Size()
	if ip == nil || bits != 32 || ones >= 31 {
		return nil
	}
	out := make(net.IP, 4)
	for i := range out {
		out[i] = ip[i] | ^n.Mask[i]
	}
	return out
}

func (l *LAN) receive(c *net.UDPConn) {
	b := make([]byte, 4097)
	for {
		n, from, err := c.ReadFromUDP(b)
		if err != nil {
			l.mu.Lock()
			if l.listener == c {
				l.listener = nil
			}
			l.mu.Unlock()
			return
		}
		if n <= 4096 {
			l.handle(b[:n], from.IP)
		}
	}
}

func (l *LAN) handle(raw []byte, sender net.IP) {
	if sender.To4() == nil || !sender.IsPrivate() {
		return
	}
	var envelope struct {
		Msg struct {
			Cmd  string
			Data json.RawMessage
		}
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	switch envelope.Msg.Cmd {
	case "scan":
		var d struct{ Device, SKU, IP string }
		if json.Unmarshal(envelope.Msg.Data, &d) != nil {
			return
		}
		id := strings.ToUpper(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(d.Device)))
		if len(id) != 12 && len(id) != 16 {
			return
		}
		for _, c := range id {
			if !strings.ContainsRune("0123456789ABCDEF", c) {
				return
			}
		}
		if d.SKU == "" || len(d.SKU) > 32 {
			return
		}
		// Route only to the source address, never an arbitrary advertised IP.
		if d.IP != "" && !net.ParseIP(d.IP).Equal(sender) {
			return
		}
		if l.onDevice != nil {
			l.onDevice(LANDevice{ID: id, Model: d.SKU, IP: sender.String()})
		}
	case "devStatus":
		var s struct {
			OnOff      *int
			Brightness *int
		}
		if json.Unmarshal(envelope.Msg.Data, &s) != nil || s.OnOff == nil || s.Brightness == nil {
			return
		}
		if *s.OnOff < 0 || *s.OnOff > 1 || *s.Brightness < 0 || *s.Brightness > 100 {
			return
		}
		if l.onStatus != nil {
			l.onStatus(sender.String(), *s.OnOff == 1, *s.Brightness)
		}
	}
}

func (l *LAN) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.listener != nil {
		_ = l.listener.Close()
	}
}
