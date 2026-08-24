package tapo

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// DiscoveryPort is where current Tapo hardware answers. The legacy Kasa port
// (9999, XOR obfuscated) is a different protocol and is not spoken here.
const DiscoveryPort = 20002

// discoveryProbe is the fixed 16-byte query newer TP-Link devices answer on
// port 20002. The reply is 16 bytes of header followed by JSON.
var discoveryProbe = mustHex("020000010000000000000000463cb5d3")

// Found is one plug seen on the LAN.
type Found struct {
	IP    string
	MAC   string
	Model string
	Name  string
	// EncryptType is what the device says it speaks: "KLAP" for current Tapo
	// plugs, "AES" for older securePassthrough firmware. It is the only
	// reliable way to tell them apart — there is no negotiation on port 80 —
	// so it is surfaced rather than assumed.
	EncryptType string
}

// SupportsKLAP reports whether this device speaks the protocol implemented
// here. An "AES" device is a real Tapo plug that needs the older transport.
func (f Found) SupportsKLAP() bool {
	return strings.EqualFold(strings.TrimSpace(f.EncryptType), "KLAP")
}

type discoveryReply struct {
	Result struct {
		DeviceID      string `json:"device_id"`
		DeviceType    string `json:"device_type"`
		DeviceModel   string `json:"device_model"`
		Alias         string `json:"alias"`
		IP            string `json:"ip"`
		MAC           string `json:"mac"`
		EncryptScheme struct {
			EncryptType string `json:"encrypt_type"`
			HTTPPort    int    `json:"http_port"`
		} `json:"mgt_encrypt_schm"`
	} `json:"result"`
}

// Discover broadcasts on UDP 20002 and collects replies until the window
// closes. Plugs that do not answer broadcast (some subnets drop it) can still
// be added by IP.
func Discover(window time.Duration) ([]Found, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("tapo: discovery socket: %w", err)
	}
	defer conn.Close()

	targets := []*net.UDPAddr{{IP: net.IPv4bcast, Port: DiscoveryPort}}
	// Per-interface broadcast as well: a host on several subnets only reaches
	// its own segment via 255.255.255.255.
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifi := range ifaces {
			if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagBroadcast == 0 {
				continue
			}
			addrs, _ := ifi.Addrs()
			for _, a := range addrs {
				n, ok := a.(*net.IPNet)
				if !ok || n.IP.To4() == nil {
					continue
				}
				if b := broadcastOf(n); b != nil {
					targets = append(targets, &net.UDPAddr{IP: b, Port: DiscoveryPort})
				}
			}
		}
	}
	for _, t := range targets {
		_, _ = conn.WriteToUDP(discoveryProbe, t)
	}

	_ = conn.SetReadDeadline(time.Now().Add(window))
	seen := map[string]Found{}
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // deadline reached: normal end of a sweep
		}
		f, ok := parseDiscovery(buf[:n], addr)
		if !ok || seen[f.IP].IP != "" {
			continue
		}
		seen[f.IP] = f
	}

	out := make([]Found, 0, len(seen))
	for _, f := range seen {
		out = append(out, f)
	}
	return out, nil
}

func parseDiscovery(raw []byte, addr *net.UDPAddr) (Found, bool) {
	if len(raw) <= 16 {
		return Found{}, false
	}
	var reply discoveryReply
	if err := json.Unmarshal(raw[16:], &reply); err != nil {
		return Found{}, false
	}
	r := reply.Result
	ip := strings.TrimSpace(r.IP)
	if ip == "" && addr != nil {
		ip = addr.IP.String()
	}
	if ip == "" {
		return Found{}, false
	}
	// Only plugs. Bulbs and hubs answer the same probe.
	if !strings.Contains(strings.ToUpper(r.DeviceType), "PLUG") &&
		!strings.HasPrefix(strings.ToUpper(r.DeviceModel), "P1") {
		return Found{}, false
	}
	return Found{
		IP:          ip,
		MAC:         strings.ToUpper(strings.ReplaceAll(r.MAC, "-", ":")),
		Model:       r.DeviceModel,
		Name:        decodeBase64Name(r.Alias),
		EncryptType: r.EncryptScheme.EncryptType,
	}, true
}

// decodeBase64Name unwraps the base64 Tapo uses for user-visible names. A
// value that is not base64 is returned as-is: firmware differs on this.
func decodeBase64Name(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		if d := strings.TrimSpace(string(b)); d != "" && isPrintable(d) {
			return d
		}
	}
	return s
}

func isPrintable(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return false
		}
	}
	return true
}

func broadcastOf(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	if ip == nil || len(n.Mask) != 4 {
		return nil
	}
	out := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		out[i] = ip[i] | ^n.Mask[i]
	}
	return out
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("tapo: bad probe literal: " + err.Error())
	}
	return b
}
