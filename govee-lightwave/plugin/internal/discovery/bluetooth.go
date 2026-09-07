package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Advertisement identity is transport-specific; do not merge with LAN based on
// name or a short MAC suffix. Discovery alone does not prove GATT light control.
type BluetoothDevice struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	RSSI         int    `json:"rssi"`
	Transport    string `json:"transport"`
	Controllable bool   `json:"controllable"`
}

var modelPattern = regexp.MustCompile(`H[0-9]{2}[0-9A-Z]{2}`)

func looksGovee(name string, services []string, companies []uint16) bool {
	for _, service := range services {
		if strings.EqualFold(service, "00010203-0405-0607-0809-0a0b0c0d1910") {
			return true
		}
	}
	for _, company := range companies {
		if company == 0xec88 {
			return true
		}
	}
	n := strings.ToUpper(name)
	for _, prefix := range []string{"IHOMENT_", "GOVEE", "GVH_", "IHOM_", "GBK_", "MINGER_"} {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return modelPattern.MatchString(n)
}

// Diagnose prints bounded, read-only scan results. It never sends power/color
// commands, pairs devices, or modifies the plugin's saved configuration.
func Diagnose() {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	var mu sync.Mutex
	print := func(kind string, value any) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(map[string]any{"type": kind, "data": value})
		fmt.Println(string(b))
	}
	l := NewLAN(func(d LANDevice) { print("lan", d) }, nil)
	defer l.Close()
	if err := l.Scan(); err != nil {
		print("lanStatus", err.Error())
	} else {
		print("lanStatus", "Listening on UDP 4002")
	}
	done := make(chan struct{})
	go func() {
		ScanBluetooth(ctx, func(d BluetoothDevice) { print("bluetooth", d) }, func(s string) { print("bluetoothStatus", s) })
		close(done)
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}
