package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestUnavailableTargetNeverControlsAllLights(t *testing.T) {
	a := &app{}
	a.db.Devices = map[string]Device{"lan": {ID: "lan"}}
	for _, target := range []string{"missing", "ble:nearby"} {
		if len(a.members(target)) != 0 {
			t.Fatalf("%s selected unrelated lights", target)
		}
	}
	if len(a.members("lan")) != 1 {
		t.Fatal("individual target lost")
	}
}

// Simulates the host receiving an inspector update, including client masking.
func TestCatalogRoutesToOpenInspector(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	a := &app{sd: &conn{c: client}, inspectors: map[string]string{"key-1": actPower}}
	a.db.Devices = map[string]Device{"light-1": {ID: "light-1", Name: "Desk"}}
	done := make(chan struct{})
	go func() { a.publishCatalog(); close(done) }()
	r := bufio.NewReader(server)
	h := make([]byte, 2)
	if _, err := io.ReadFull(r, h); err != nil {
		t.Fatal(err)
	}
	n := int(h[1] & 127)
	if n == 126 {
		b := make([]byte, 2)
		_, _ = io.ReadFull(r, b)
		n = int(binary.BigEndian.Uint16(b))
	}
	mask := make([]byte, 4)
	_, _ = io.ReadFull(r, mask)
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] ^= mask[i%4]
	}
	var message struct {
		Event, Action, Context string
		Payload                struct{ Devices []Device }
	}
	if err := json.Unmarshal(b, &message); err != nil {
		t.Fatal(err)
	}
	if message.Event != "sendToPropertyInspector" || message.Action != actPower || message.Context != "key-1" {
		t.Fatalf("host cannot route catalog: %+v", message)
	}
	if len(message.Payload.Devices) != 1 || message.Payload.Devices[0].Name != "Desk" {
		t.Fatal("catalog omitted newly available device")
	}
	<-done
}
