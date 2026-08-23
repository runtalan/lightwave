package govee

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// BLE addressing. The app passes device addresses around as opaque strings
// (slots.json, the pool, the brightness pump), so Bluetooth peripherals reuse
// the same field with a "ble:" prefix in front of the CoreBluetooth UUID.
// macOS masks real MAC addresses; the UUID is the stable per-Mac identifier.
const BLEPrefix = "ble:"

func IsBLE(addr string) bool {
	return strings.HasPrefix(strings.TrimSpace(addr), BLEPrefix)
}

// BLEAddrUUID extracts the CoreBluetooth UUID from a ble: address.
func BLEAddrUUID(addr string) string {
	return strings.TrimPrefix(strings.TrimSpace(addr), BLEPrefix)
}

// bleSendFn is installed by the darwin BLE manager when it starts. Keeping the
// hook here lets SendTurn/SendBrightness/SendColor dispatch without the
// portable code importing CoreBluetooth.
var (
	bleSendMu sync.RWMutex
	bleSendFn func(addr string, pkt []byte) error
)

func setBLESender(f func(addr string, pkt []byte) error) {
	bleSendMu.Lock()
	bleSendFn = f
	bleSendMu.Unlock()
}

func bleSend(addr string, pkt []byte) error {
	bleSendMu.RLock()
	f := bleSendFn
	bleSendMu.RUnlock()
	if f == nil {
		return fmt.Errorf("govee: ble transport not running")
	}
	return f(addr, pkt)
}

// Govee BLE frames are exactly 20 bytes: command bytes at the front, zero
// padding through index 18, and an XOR checksum of the first 19 bytes at
// index 19.
func blePacket(cmd []byte) []byte {
	pkt := make([]byte, 20)
	copy(pkt, cmd)
	var sum byte
	for _, b := range pkt[:19] {
		sum ^= b
	}
	pkt[19] = sum
	return pkt
}

// blePacketKeepAlive is the heartbeat that keeps a Govee peripheral from
// dropping the connection.
func blePacketKeepAlive() []byte {
	return blePacket([]byte{0xAA, 0x01})
}

func blePacketPower(on bool) []byte {
	v := byte(0)
	if on {
		v = 1
	}
	return blePacket([]byte{0x33, 0x01, v})
}

// blePacketBrightness sends brightness as a 1-100 percentage.
//
// Do not "helpfully" rescale this to 0-255. RGBIC strips (H617A and kin) treat
// the byte as a percentage, and a value above 100 spills into the controller's
// colour/scene state — the lamp visibly changes hue while you are only moving
// the brightness slider. 1-100 is the range Govee's own BLE traffic uses and is
// accepted by both the RGBIC strips and the older single-zone lamps.
func blePacketBrightness(percent int) []byte {
	if percent < 1 {
		percent = 1
	}
	if percent > 100 {
		percent = 100
	}
	return blePacket([]byte{0x33, 0x04, byte(percent)})
}

// blePacketColorLegacy sets the whole lamp via manual-color mode 0x02 —
// the command classic single-zone bulbs and strips understand.
func blePacketColorLegacy(r, g, b int) []byte {
	return blePacket([]byte{0x33, 0x05, 0x02, byte(r), byte(g), byte(b)})
}

// blePacketColorSegment drives RGBIC models, which ignore mode 0x02: mode
// 0x15 sub 0x01 with a two-byte bitmask selecting every segment.
func blePacketColorSegment(r, g, b int) []byte {
	return blePacket([]byte{0x33, 0x05, 0x15, 0x01, byte(r), byte(g), byte(b),
		0x00, 0x00, 0x00, 0x00, 0x00, 0xFF, 0x7F})
}

// Discovery helpers.

var (
	bleGoveePrefix = regexp.MustCompile(`(?i)^(ihoment_|govee_|gvh_|ihom_|gbk_)`)
	bleModelRe     = regexp.MustCompile(`H[0-9]{2}[0-9A-Z]{2}`)
	bleSuffixRe    = regexp.MustCompile(`([0-9A-F]{4})$`)
)

// bleNameLooksGovee reports whether an advertised local name belongs to a
// Govee light: a known vendor prefix, or a model code like H6168 anywhere in
// the name.
func bleNameLooksGovee(name string) bool {
	if name == "" {
		return false
	}
	return bleGoveePrefix.MatchString(name) || bleModelRe.MatchString(strings.ToUpper(name))
}

// bleModelFromName pulls the model code (e.g. "H6168") out of an advertised
// name like "ihoment_H6168_3A4B".
func bleModelFromName(name string) string {
	return bleModelRe.FindString(strings.ToUpper(name))
}

// bleSuffixFromName returns the trailing hex chunk of the advertised name —
// conventionally the tail of the device MAC, which also appears inside the
// cloud device ID. It is the only cross-reference macOS leaves us for
// matching a BLE peripheral to its cloud identity (and friendly name).
func BLESuffixFromName(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	parts := strings.Split(name, "_")
	tail := parts[len(parts)-1]
	// A name that ends in the model code ("GVH_H61E5") has no MAC tail;
	// treating "61E5" as one could mis-match an unrelated cloud device.
	if bleModelRe.FindString(tail) == tail {
		return ""
	}
	return bleSuffixRe.FindString(tail)
}

// BLEDeviceID synthesizes a catalog ID for a peripheral that could not be
// matched to a cloud device.
func BLEDeviceID(uuid string) string {
	return "BLE" + NormalizeID(strings.ReplaceAll(uuid, "-", ""))
}

// BLEFallbackName labels a peripheral with no cloud identity to take a
// friendly name from. The MAC tail keeps several same-model strips apart.
func BLEFallbackName(model, suffix string) string {
	switch {
	case model != "" && suffix != "":
		return model + "-" + suffix
	case model != "":
		return model
	}
	return "Govee BLE"
}
