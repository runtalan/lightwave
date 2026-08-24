package govee

import (
	"bytes"
	"testing"
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
	if SupportsSegments("H6001") || SupportsSegments("H6072") {
		t.Fatal("bulbs and floor lamps must not advertise strip segments")
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
