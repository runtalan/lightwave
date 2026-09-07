package govee

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Govee's Bluetooth company ID (little-endian 0xEC88 in advertisement data).
const goveeCompanyID uint16 = 0xEC88

// bleLooksGoveeAdv is the advertisement-side gate used by the Windows scanner
// (Darwin does the same checks in Objective-C before calling into Go). A
// name-only filter drops H6001, which often omits the GAP name on the first
// packet and only carries manufacturer 0xEC88 / service 1910.
func bleLooksGoveeAdv(name string, serviceUUIDs []string, companyIDs []uint16) bool {
	if bleNameLooksGovee(name) {
		return true
	}
	for _, u := range serviceUUIDs {
		u = strings.ToUpper(u)
		if strings.HasSuffix(u, "1910") || strings.Contains(u, "0A0B0C0D1910") {
			return true
		}
	}
	for _, id := range companyIDs {
		if id == goveeCompanyID {
			return true
		}
	}
	return false
}

// BLE addressing. The app passes device addresses around as opaque strings
// (slots.json, the pool, the brightness pump), so Bluetooth peripherals reuse
// the same field with a "ble:" prefix. On macOS that is a CoreBluetooth UUID
// (the OS masks MACs); on Windows it is the Bluetooth address. Either way it
// is stable on that machine, and BLEBindingMatch still pairs a pad by the
// advertised MAC tail in the name.
const BLEPrefix = "ble:"

func IsBLE(addr string) bool {
	return strings.HasPrefix(strings.TrimSpace(addr), BLEPrefix)
}

// BLEAddrUUID extracts the platform identifier from a ble: address.
func BLEAddrUUID(addr string) string {
	return strings.TrimPrefix(strings.TrimSpace(addr), BLEPrefix)
}

// bleSendFn is installed by the BLE manager when it starts. Keeping the
// hook here lets SendTurn/SendBrightness/SendColor dispatch without the
// portable code importing a platform radio.
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

func bleIsKeepAlive(pkt []byte) bool {
	return len(pkt) >= 2 && pkt[0] == 0xAA && pkt[1] == 0x01
}

// BLELinkWeak reports a far / flaky advertiser. RSSI 0 means "unknown" (a
// connected harvest with no advertisement) and is not treated as weak.
func BLELinkWeak(rssi int) bool {
	return rssi != 0 && rssi <= -85
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

// blePacketBrightness255 is the 0–255 brightness scale older single-zone
// bulbs (H6001 and kin) expect. Do not send this to RGBIC strips: a byte
// above 100 leaks into their colour state.
func blePacketBrightness255(percent int) []byte {
	if percent < 1 {
		percent = 1
	}
	if percent > 100 {
		percent = 100
	}
	v := (percent*255 + 50) / 100
	if v < 1 {
		v = 1
	}
	if v > 255 {
		v = 255
	}
	return blePacket([]byte{0x33, 0x04, byte(v)})
}

// blePacketColorLegacy sets the whole lamp via manual-color mode 0x02 —
// the command classic single-zone bulbs and strips understand.
func blePacketColorLegacy(r, g, b int) []byte {
	return blePacket([]byte{0x33, 0x05, 0x02, byte(r), byte(g), byte(b)})
}

// blePacketColorRGBWW is the RGBWW form (mode 0x0b) some H600x bulbs want
// instead of — or in addition to — 0x02. WW/CW of 0 keeps the RGB diodes on.
func blePacketColorRGBWW(r, g, b, ww, cw int) []byte {
	return blePacket([]byte{0x33, 0x05, 0x0b, byte(r), byte(g), byte(b), 0, 0, 0, byte(ww), byte(cw)})
}

// bleSegments is how many addressable zones the segment command reaches: the
// two mask bytes carry 15 usable bits (0x7FFF), one per zone.
const bleSegments = 15

// allSegments selects every zone — the whole-strip mask.
const allSegments uint16 = 0x7FFF

// GradientBands is how many colours a strip scene is painted with. Each band
// becomes one BLE write, so this trades smoothness against radio time: 8 reads
// as a continuous ramp across a strip while leaving the 900ms dance step ample
// room, even with several strips sharing the adapter.
const GradientBands = 8

// blePacketColorSegmentMask drives RGBIC models, which ignore mode 0x02: mode
// 0x15 sub 0x01, with the trailing two bytes selecting which zones the colour
// applies to. Addressing subsets is what makes a multi-colour scene possible —
// one write per band rather than one colour for the whole strip.
func blePacketColorSegmentMask(r, g, b int, mask uint16) []byte {
	return blePacket([]byte{0x33, 0x05, 0x15, 0x01, byte(r), byte(g), byte(b),
		0x00, 0x00, 0x00, 0x00, 0x00, byte(mask), byte(mask >> 8)})
}

// blePacketColorSegment paints every zone one colour.
func blePacketColorSegment(r, g, b int) []byte {
	return blePacketColorSegmentMask(r, g, b, allSegments)
}

// bleSegmentMasks splits the strip into n contiguous bands and returns one
// zone-selection mask per band. Every zone lands in exactly one band, so a
// scene covers the whole strip with no gaps and no zone written twice.
func bleSegmentMasks(n int) []uint16 {
	if n <= 0 {
		return nil
	}
	if n > bleSegments {
		n = bleSegments
	}
	out := make([]uint16, n)
	for zone := 0; zone < bleSegments; zone++ {
		out[zone*n/bleSegments] |= 1 << uint(zone)
	}
	return out
}

// Discovery helpers.

var (
	bleGoveePrefix = regexp.MustCompile(`(?i)^(ihoment_|govee[_ ]|gvh_|ihom_|gbk_|minger_)`)
	bleModelRe     = regexp.MustCompile(`H[0-9]{2}[0-9A-Z]{2}`)
	bleSuffixRe    = regexp.MustCompile(`([0-9A-F]{4})$`)
	bleCloseName   = regexp.MustCompile(`(?i)h60|govee|ihom|minger|gbk_|gvh_`)
)

// bleNameLooksGovee reports whether an advertised local name belongs to a
// Govee light: a known vendor prefix, or a model code like H6001 / H6168
// anywhere in the name (including the concatenated "H6001C883" form).
func bleNameLooksGovee(name string) bool {
	if name == "" {
		return false
	}
	return bleGoveePrefix.MatchString(name) || bleModelRe.MatchString(strings.ToUpper(name))
}

// bleAcceptFound is the Go-side gate for CoreBluetooth hits. Empty names and
// the "Govee BLE" placeholder are allowed because ObjC already matched Govee
// manufacturer data / service 1910 — H6001 often advertises that way before
// the local name arrives.
func bleAcceptFound(name string) bool {
	if name == "" || strings.EqualFold(strings.TrimSpace(name), "Govee BLE") {
		return true
	}
	return bleNameLooksGovee(name)
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

// BLEBindingMatch reports whether a discovered peripheral is the lamp bound
// to a pad. CoreBluetooth UUIDs are per-Mac and can go stale; the advertised
// MAC tail (C883 in "H6001-C883" / "ihoment_H6001_C883") is the stable handle,
// including the identity the Govee app shows as H6001C883.
func BLEBindingMatch(devModel, advName, slotModel, slotDeviceID, slotName, slotCustom string) bool {
	if strings.TrimSpace(devModel) == "" || !strings.EqualFold(strings.TrimSpace(devModel), strings.TrimSpace(slotModel)) {
		return false
	}
	suffix := BLESuffixFromName(advName)
	if suffix == "" {
		return false
	}
	blob := strings.ToUpper(slotDeviceID + " " + slotName + " " + slotCustom)
	return strings.Contains(blob, suffix)
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
