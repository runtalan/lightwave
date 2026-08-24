package govee

import (
	"bytes"
	"strings"
	"testing"

	"lightwave/internal/color"
)

func xorCheck(t *testing.T, pkt []byte) {
	t.Helper()
	if len(pkt) != 20 {
		t.Fatalf("packet length = %d, want 20", len(pkt))
	}
	var sum byte
	for _, b := range pkt[:19] {
		sum ^= b
	}
	if pkt[19] != sum {
		t.Fatalf("checksum = %#x, want %#x", pkt[19], sum)
	}
}

func TestBLEPacketKeepAlive(t *testing.T) {
	pkt := blePacketKeepAlive()
	xorCheck(t, pkt)
	want := make([]byte, 20)
	want[0], want[1], want[19] = 0xAA, 0x01, 0xAB
	if !bytes.Equal(pkt, want) {
		t.Fatalf("keepalive = % x", pkt)
	}
}

func TestBLEPacketPower(t *testing.T) {
	on := blePacketPower(true)
	xorCheck(t, on)
	if on[0] != 0x33 || on[1] != 0x01 || on[2] != 0x01 || on[19] != 0x33 {
		t.Fatalf("power on = % x", on)
	}
	off := blePacketPower(false)
	xorCheck(t, off)
	if off[2] != 0x00 || off[19] != 0x32 {
		t.Fatalf("power off = % x", off)
	}
}

func TestBLEPacketBrightness(t *testing.T) {
	full := blePacketBrightness(100)
	xorCheck(t, full)
	if full[2] != 100 {
		t.Fatalf("brightness 100 payload = %d, want 100", full[2])
	}
	dim := blePacketBrightness(0) // clamped to 1%
	xorCheck(t, dim)
	if dim[2] != 1 {
		t.Fatalf("brightness floor = %d, want 1", dim[2])
	}
	// Never exceed 100: RGBIC firmware reads a larger byte into its colour
	// state, which shifts the lamp's hue during a brightness change.
	for _, p := range []int{50, 99, 100, 150, 255, 1000} {
		if v := blePacketBrightness(p)[2]; v < 1 || v > 100 {
			t.Fatalf("brightness(%d) payload = %d, outside 1-100", p, v)
		}
	}
}

func TestBLEPacketBrightness255(t *testing.T) {
	pkt := blePacketBrightness255(100)
	xorCheck(t, pkt)
	if pkt[2] != 255 {
		t.Fatalf("brightness255(100) = %d, want 255", pkt[2])
	}
	dim := blePacketBrightness255(1)
	if dim[2] < 1 {
		t.Fatal("brightness255 floor vanished")
	}
	off := blePacketBrightness255(0)
	if off[2] < 1 {
		t.Fatalf("brightness255(0) payload = %d; 0 turns H6001 off", off[2])
	}
	mid := blePacketBrightness255(50)
	if mid[2] < 1 || mid[2] == 0 {
		t.Fatalf("brightness255(50) = %d, looks like off", mid[2])
	}
}

func TestBLEPacketColorRGBWW(t *testing.T) {
	pkt := blePacketColorRGBWW(10, 20, 30, 0, 0)
	xorCheck(t, pkt)
	if pkt[2] != 0x0b || pkt[3] != 10 || pkt[4] != 20 || pkt[5] != 30 {
		t.Fatalf("rgbww = % x", pkt[:7])
	}
}

func TestModelClass(t *testing.T) {
	if !IsClassicBulb("H6001") || !IsClassicBulb("h6001") || IsRGBIC("H6001") {
		t.Fatal("H6001 must be a classic bulb, not RGBIC")
	}
	if !IsRGBIC("H617A") || IsClassicBulb("H617A") || !SupportsSegments("H617A") {
		t.Fatal("H617A must be RGBIC")
	}
	if SupportsSegments("H6001") {
		t.Fatal("classic bulbs must not advertise strip segments")
	}
	// H6072 Lyra is a multi-zone floor lamp, same family as RGBIC strips.
	if !IsRGBIC("H6072") || IsClassicBulb("H6072") || !SupportsSegments("H6072") {
		t.Fatal("H6072 Lyra must be RGBIC and take the segment gradient path")
	}
	if !IsRGBIC("H61E5") || !IsRGBIC("H6168") {
		t.Fatal("LAN RGBIC strips must take the segment path")
	}
}

func TestSendColorClassicBulbNoSegment(t *testing.T) {
	var got [][]byte
	setBLESender(func(addr string, pkt []byte) error {
		got = append(got, append([]byte(nil), pkt...))
		return nil
	})
	t.Cleanup(func() { setBLESender(nil) })
	Remember("ble:h6001", "H6001")
	if err := SendColor("ble:h6001", 255, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no packets sent")
	}
	sawLegacy, sawRGBWW := false, false
	for _, p := range got {
		if len(p) < 3 {
			continue
		}
		if p[1] == 0x05 && p[2] == 0x15 {
			t.Fatalf("H6001 must not receive RGBIC segment packets: % x", p)
		}
		if p[1] == 0x05 && p[2] == 0x02 {
			sawLegacy = true
		}
		if p[1] == 0x05 && p[2] == 0x0b {
			sawRGBWW = true
		}
	}
	if !sawLegacy || !sawRGBWW {
		t.Fatalf("H6001 expected 0x02 and 0x0b, got %d packets", len(got))
	}
}

func TestBLEPacketColor(t *testing.T) {
	leg := blePacketColorLegacy(255, 0, 0)
	xorCheck(t, leg)
	if leg[0] != 0x33 || leg[1] != 0x05 || leg[2] != 0x02 || leg[3] != 0xFF || leg[19] != 0xCB {
		t.Fatalf("legacy color = % x", leg)
	}
	seg := blePacketColorSegment(10, 20, 30)
	xorCheck(t, seg)
	if seg[2] != 0x15 || seg[3] != 0x01 || seg[12] != 0xFF || seg[13] != 0x7F {
		t.Fatalf("segment color = % x", seg)
	}
}

func TestBLEAddrHelpers(t *testing.T) {
	addr := "ble:12345678-ABCD-1234-ABCD-123456789ABC"
	if !IsBLE(addr) || IsBLE("192.0.2.23") || IsBLE("") {
		t.Fatal("IsBLE misclassified")
	}
	if BLEAddrUUID(addr) != "12345678-ABCD-1234-ABCD-123456789ABC" {
		t.Fatalf("BLEAddrUUID = %q", BLEAddrUUID(addr))
	}
}

func TestBLENameParsing(t *testing.T) {
	for _, name := range []string{"ihoment_H6168_3A4B", "Govee_H605C_11FF", "GBK_H6072_2C01", "GVH_H61E5"} {
		if !bleNameLooksGovee(name) {
			t.Fatalf("%q not recognized as Govee", name)
		}
	}
	if bleNameLooksGovee("AirPods Pro") || bleNameLooksGovee("") {
		t.Fatal("non-Govee name recognized")
	}
	if m := bleModelFromName("ihoment_H6168_3A4B"); m != "H6168" {
		t.Fatalf("model = %q", m)
	}
	if s := BLESuffixFromName("ihoment_H6168_3A4B"); s != "3A4B" {
		t.Fatalf("suffix = %q", s)
	}
	// A name that ends in the model code has no MAC tail; fabricating one
	// could mis-match an unrelated cloud device.
	if s := BLESuffixFromName("GVH_H61E5"); s != "" {
		t.Fatalf("suffix from tailless name = %q", s)
	}
	if m := bleModelFromName("H6001C883"); m != "H6001" {
		t.Fatalf("concatenated model = %q", m)
	}
	if s := BLESuffixFromName("H6001C883"); s != "C883" {
		t.Fatalf("concatenated suffix = %q", s)
	}
	for _, name := range []string{
		"ihoment_H6001_C883", "H6001-C883", "H6001C883", "minger_H6001_C883", "Govee_H6001_11FF",
	} {
		if !bleNameLooksGovee(name) {
			t.Fatalf("%q not recognized as Govee", name)
		}
		if bleModelFromName(name) != "H6001" {
			t.Fatalf("%q model = %q, want H6001", name, bleModelFromName(name))
		}
	}
	if !bleAcceptFound("") || !bleAcceptFound("Govee BLE") {
		t.Fatal("nameless / placeholder Govee ads must be accepted")
	}
}

func TestBLEBindingMatchClaudiaBulb(t *testing.T) {
	if !BLEBindingMatch("H6001", "ihoment_H6001_C883", "H6001", "BLEDEAD", "H6001-C883", "ClaudiaBulb") {
		t.Fatal("pad 7 must match H6001 advertised as C883 even if the CoreBluetooth UUID is stale")
	}
	if BLEBindingMatch("H617A", "ihoment_H617A_7B46", "H6001", "BLEDEAD", "H6001-C883", "ClaudiaBulb") {
		t.Fatal("strip must not steal the bulb pad")
	}
	if BLEBindingMatch("H6001", "ihoment_H6001_FFFF", "H6001", "BLEDEAD", "H6001-C883", "ClaudiaBulb") {
		t.Fatal("a different H6001 tail must not match ClaudiaBulb")
	}
}

func TestSendTurnBLEPowerPacket(t *testing.T) {
	var got [][]byte
	setBLESender(func(addr string, pkt []byte) error {
		got = append(got, append([]byte(nil), pkt...))
		return nil
	})
	t.Cleanup(func() { setBLESender(nil) })
	Remember("ble:claudia", "H6001")
	if err := SendTurn("ble:claudia", true); err != nil {
		t.Fatal(err)
	}
	if err := SendTurn("ble:claudia", false); err != nil {
		t.Fatal(err)
	}
	var sawOn, sawOff bool
	for _, p := range got {
		if len(p) < 3 {
			continue
		}
		if p[1] == 0x05 && p[2] == 0x15 {
			t.Fatalf("H6001 power path must not send RGBIC 0x15: % x", p)
		}
		if p[0] == 0x33 && p[1] == 0x01 && p[2] == 0x01 {
			sawOn = true
			xorCheck(t, p)
			if p[19] != 0x33 {
				t.Fatalf("power on checksum = %#x", p[19])
			}
		}
		if p[0] == 0x33 && p[1] == 0x01 && p[2] == 0x00 {
			sawOff = true
			xorCheck(t, p)
		}
	}
	if !sawOn || !sawOff {
		t.Fatalf("missing power packets on=%v off=%v n=%d", sawOn, sawOff, len(got))
	}
}

func TestBLESegmentMasks(t *testing.T) {
	// Every zone must land in exactly one band: a gap leaves part of the strip
	// on its previous colour, an overlap writes a zone twice.
	for _, bands := range []int{1, 2, 3, 5, 8, 15} {
		masks := bleSegmentMasks(bands)
		if len(masks) != bands {
			t.Fatalf("bands=%d: got %d masks", bands, len(masks))
		}
		var union uint16
		for i, m := range masks {
			if m == 0 {
				t.Fatalf("bands=%d: band %d selects no zone", bands, i)
			}
			if union&m != 0 {
				t.Fatalf("bands=%d: band %d overlaps an earlier band", bands, i)
			}
			union |= m
		}
		if union != allSegments {
			t.Fatalf("bands=%d: coverage = %#x, want %#x", bands, union, allSegments)
		}
	}

	// Bands must be contiguous runs, so the gradient reads along the strip
	// instead of scattering colours across it.
	for _, m := range bleSegmentMasks(4) {
		trimmed := m
		for trimmed&1 == 0 {
			trimmed >>= 1
		}
		if trimmed&(trimmed+1) != 0 {
			t.Fatalf("band mask %#x is not a contiguous run", m)
		}
	}

	if got := bleSegmentMasks(0); got != nil {
		t.Fatalf("zero bands = %v, want nil", got)
	}
	// More bands than zones is clamped rather than producing empty bands.
	if got := len(bleSegmentMasks(64)); got != bleSegments {
		t.Fatalf("oversized bands = %d, want %d", got, bleSegments)
	}
}

func TestBLEPacketColorSegmentMask(t *testing.T) {
	pkt := blePacketColorSegmentMask(1, 2, 3, 0x0F0)
	xorCheck(t, pkt)
	if pkt[2] != 0x15 || pkt[3] != 0x01 {
		t.Fatalf("segment mode = % x", pkt[:4])
	}
	if pkt[4] != 1 || pkt[5] != 2 || pkt[6] != 3 {
		t.Fatalf("colour payload = % x", pkt[4:7])
	}
	if pkt[12] != 0xF0 || pkt[13] != 0x00 {
		t.Fatalf("mask bytes = % x, want f0 00", pkt[12:14])
	}
	// The whole-strip helper must stay byte-identical to the mask form, since
	// that packet shape is the one known to work on real hardware.
	if !bytes.Equal(blePacketColorSegment(9, 8, 7), blePacketColorSegmentMask(9, 8, 7, allSegments)) {
		t.Fatal("whole-strip packet diverged from the masked form")
	}
}

func TestSendGradientRGBICUsesSegments(t *testing.T) {
	var got [][]byte
	setBLESender(func(addr string, pkt []byte) error {
		got = append(got, append([]byte(nil), pkt...))
		return nil
	})
	t.Cleanup(func() { setBLESender(nil) })

	cols := []color.RGBK{
		{R: 255}, {R: 200, G: 40}, {G: 180}, {B: 220},
		{R: 80, B: 200}, {R: 255, G: 80}, {G: 40, B: 180}, {R: 40, G: 200, B: 40},
	}
	for _, sku := range []string{"H617A", "H6072"} {
		got = nil
		addr := "ble:" + strings.ToLower(sku)
		Remember(addr, sku)
		if err := SendGradient(addr, cols); err != nil {
			t.Fatal(err)
		}
		if len(got) < 2 {
			t.Fatalf("%s: want several segment packets, got %d", sku, len(got))
		}
		masks := map[uint16]bool{}
		for _, p := range got {
			if len(p) < 14 {
				continue
			}
			if p[1] == 0x05 && p[2] == 0x02 {
				t.Fatalf("%s: gradient must not send solid 0x02: % x", sku, p)
			}
			if p[1] == 0x05 && p[2] == 0x15 {
				masks[uint16(p[12])|uint16(p[13])<<8] = true
			}
		}
		if len(masks) < 2 {
			t.Fatalf("%s: gradient used %d zone masks, want a ramp", sku, len(masks))
		}
	}

	got = nil
	Remember("ble:h6001", "H6001")
	if err := SendGradient("ble:h6001", cols); err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if len(p) >= 3 && p[1] == 0x05 && p[2] == 0x15 {
			t.Fatalf("H6001 must not receive segment packets: % x", p)
		}
	}
}

func TestSendColorUnknownSKUNoSegment(t *testing.T) {
	var got [][]byte
	setBLESender(func(addr string, pkt []byte) error {
		got = append(got, append([]byte(nil), pkt...))
		return nil
	})
	t.Cleanup(func() { setBLESender(nil) })
	if err := SendColor("ble:unknown", 8, 16, 24, 0); err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if len(p) >= 3 && p[1] == 0x05 && p[2] == 0x15 {
			t.Fatalf("unknown SKU must not receive 0x15: % x", p)
		}
	}
}

func TestSendGradientLANNoSolidOverwrite(t *testing.T) {
	var payloads []string
	testControlSink = func(ip, payload string) {
		payloads = append(payloads, payload)
	}
	t.Cleanup(func() { testControlSink = nil })
	Remember("192.0.2.23", "H6072")
	cols := make([]color.RGBK, 8)
	for i := range cols {
		cols[i] = color.RGBK{R: i * 10, G: 40, B: 200 - i*10}
	}
	if err := SendGradient("192.0.2.23", cols); err != nil {
		t.Fatal(err)
	}
	if len(payloads) == 0 {
		t.Fatal("H6072 LAN gradient sent nothing")
	}
	sawPt, sawColor := false, false
	for _, p := range payloads {
		if strings.Contains(p, "ptReal") {
			sawPt = true
		}
		if strings.Contains(p, `"cmd":"color"`) || strings.Contains(p, "colorwc") {
			sawColor = true
		}
	}
	if !sawPt {
		t.Fatal("H6072 LAN gradient must send ptReal segment frames")
	}
	if sawColor {
		t.Fatal("H6072 LAN gradient must not follow ptReal with a solid color")
	}
}

func TestSendBrightnessNeverZeroOrTurnOff(t *testing.T) {
	var payloads []string
	testControlSink = func(_, payload string) {
		payloads = append(payloads, payload)
	}
	t.Cleanup(func() { testControlSink = nil })

	if err := SendBrightness("192.0.2.10", 0); err != nil {
		t.Fatal(err)
	}
	if err := SendBrightness("192.0.2.10", 100); err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 {
		t.Fatalf("payloads = %d, want 2", len(payloads))
	}
	if strings.Contains(payloads[0], `"value":0`) || strings.Contains(payloads[0], `"cmd":"turn"`) {
		t.Fatalf("fader 0 must not turn the lamp off: %s", payloads[0])
	}
	if !strings.Contains(payloads[0], `"value":1`) {
		t.Fatalf("fader 0 should send 1%%, got %s", payloads[0])
	}
	if !strings.Contains(payloads[1], `"value":100`) {
		t.Fatalf("fader 100 should send 100, got %s", payloads[1])
	}
}

func TestSendBrightnessH6001NeverZeroByte(t *testing.T) {
	var got [][]byte
	setBLESender(func(_ string, pkt []byte) error {
		got = append(got, append([]byte(nil), pkt...))
		return nil
	})
	t.Cleanup(func() { setBLESender(nil) })
	Remember("ble:claudia", "H6001")

	if err := SendBrightness("ble:claudia", 0); err != nil {
		t.Fatal(err)
	}
	if err := SendBrightness("ble:claudia", 100); err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("packets = %d", len(got))
	}
	if got[0][0] != 0x33 || got[0][1] != 0x04 {
		t.Fatalf("opcode = % x, want 33 04", got[0][:2])
	}
	if got[0][2] < 1 {
		t.Fatalf("H6001 brightness 0 payload = %d; that turns the bulb off", got[0][2])
	}
	if got[1][2] != 255 {
		t.Fatalf("H6001 100%% = %d, want 255", got[1][2])
	}
}

func TestApplyPoolBrightnessZeroIsDimNotOff(t *testing.T) {
	var payloads []string
	testControlSink = func(_, payload string) {
		payloads = append(payloads, payload)
	}
	t.Cleanup(func() { testControlSink = nil })
	lastBright.Delete("192.0.2.11")

	ApplyPoolBrightness([]string{"192.0.2.11"}, 0, false)
	if len(payloads) == 0 {
		t.Fatal("no brightness write")
	}
	for _, p := range payloads {
		if strings.Contains(p, `"cmd":"turn"`) {
			t.Fatalf("fader must not send turn: %s", p)
		}
		if strings.Contains(p, `"cmd":"color"`) || strings.Contains(p, "colorwc") {
			t.Fatalf("fader must not send color: %s", p)
		}
		if strings.Contains(p, `"value":0`) {
			t.Fatalf("fader sent brightness 0: %s", p)
		}
	}
}
