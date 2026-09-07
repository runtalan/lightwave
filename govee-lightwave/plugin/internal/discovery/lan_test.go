package discovery

import (
	"net"
	"strings"
	"testing"
)

func TestDiscoveryValidatesSenderAndIdentity(t *testing.T) {
	var found []LANDevice
	l := NewLAN(func(d LANDevice) { found = append(found, d) }, nil)
	ip := net.ParseIP("192.168.1.20")
	for _, raw := range []string{
		`not json`,
		`{"msg":{"cmd":"scan","data":{"device":"bad","sku":"H6072","ip":"192.168.1.20"}}}`,
		`{"msg":{"cmd":"scan","data":{"device":"AA:BB:CC:DD:EE:FF","sku":"H6072","ip":"8.8.8.8"}}}`,
		`{"msg":{"cmd":"other","data":{"device":"AA:BB:CC:DD:EE:FF","sku":"H6072"}}}`,
	} {
		l.handle([]byte(raw), ip)
	}
	if len(found) != 0 {
		t.Fatalf("accepted invalid discovery: %+v", found)
	}
	l.handle([]byte(`{"msg":{"cmd":"scan","data":{"device":"AA:BB:CC:DD:EE:FF","sku":"H6072"}}}`), ip)
	if len(found) != 1 || found[0].ID != "AABBCCDDEEFF" || found[0].IP != "192.168.1.20" {
		t.Fatalf("missing source-address fallback: %+v", found)
	}
}

func TestMalformedStatusNeverBecomesOff(t *testing.T) {
	calls := 0
	l := NewLAN(nil, func(_ string, on bool, brightness int) {
		calls++
		if !on || brightness != 70 {
			t.Fatal("wrong status")
		}
	})
	for _, data := range []string{`{}`, `{"onOff":1}`, `{"onOff":7,"brightness":100}`, `{"onOff":0,"brightness":101}`} {
		l.handle([]byte(`{"msg":{"cmd":"devStatus","data":`+data+`}}`), net.ParseIP("192.168.1.20"))
	}
	if calls != 0 {
		t.Fatal("malformed status accepted")
	}
	l.handle([]byte(`{"msg":{"cmd":"devStatus","data":{"onOff":1,"brightness":70}}}`), net.ParseIP("192.168.1.20"))
	if calls != 1 {
		t.Fatal("valid status dropped")
	}
}

func TestPortConflictCanRecover(t *testing.T) {
	busy, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	l := NewLAN(nil, nil)
	l.port = busy.LocalAddr().(*net.UDPAddr).Port
	defer l.Close()
	if err = l.Scan(); err == nil || !strings.Contains(err.Error(), "Close other Govee") {
		t.Fatalf("conflict not actionable: %v", err)
	}
	if err = busy.Close(); err != nil {
		t.Fatal(err)
	}
	// This may report no LAN interfaces on CI, but binding must recover.
	_ = l.Scan()
	l.mu.Lock()
	listening := l.listener != nil
	l.mu.Unlock()
	if !listening {
		t.Fatal("listener failed to retry after conflict was released")
	}
}

func TestBroadcastAddress(t *testing.T) {
	_, n, _ := net.ParseCIDR("192.168.7.32/24")
	if got := broadcastIP(n).String(); got != "192.168.7.255" {
		t.Fatal(got)
	}
	_, n, _ = net.ParseCIDR("192.168.7.32/32")
	if broadcastIP(n) != nil {
		t.Fatal("must not broadcast on host-only route")
	}
}

func TestBluetoothAdvertisementMatching(t *testing.T) {
	if !looksGovee("", nil, []uint16{0xec88}) {
		t.Fatal("nameless manufacturer match dropped")
	}
	if !looksGovee("", []string{"00010203-0405-0607-0809-0A0B0C0D1910"}, nil) {
		t.Fatal("service match dropped")
	}
	if !looksGovee("ihoment_H6001_C883", nil, nil) {
		t.Fatal("name match dropped")
	}
	if looksGovee("Headphones", nil, []uint16{0x004c}) {
		t.Fatal("unrelated device accepted")
	}
}
