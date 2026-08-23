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
