package govee

import (
	"strings"
	"sync"
)

// Address → SKU, so SendColor/SendBrightness can pick the packet the lamp
// actually speaks. Slots and discovery fill this; it is never persisted.
var (
	modelMu     sync.RWMutex
	modelByAddr = map[string]string{}
)

// Remember records the SKU for a control address (LAN IP or ble: UUID).
func Remember(addr, model string) {
	addr = strings.TrimSpace(addr)
	model = strings.ToUpper(strings.TrimSpace(model))
	if addr == "" || model == "" {
		return
	}
	modelMu.Lock()
	modelByAddr[addr] = model
	modelMu.Unlock()
}

// ModelOf returns the last SKU remembered for addr, or "".
func ModelOf(addr string) string {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return modelByAddr[strings.TrimSpace(addr)]
}

// IsClassicBulb reports single-zone RGB/RGBWW lamps (H6001 and kin). These
// speak manual-color mode 0x02 and ignore — or worse, mis-handle — RGBIC
// segment packets (0x15). Sending 0x15 after 0x02 can leave them stuck.
func IsClassicBulb(model string) bool {
	m := strings.ToUpper(strings.TrimSpace(model))
	if len(m) < 4 {
		return false
	}
	switch {
	case strings.HasPrefix(m, "H600"),
		strings.HasPrefix(m, "H604"),
		strings.HasPrefix(m, "H605"),
		strings.HasPrefix(m, "H608"),
		strings.HasPrefix(m, "H609"),
		strings.HasPrefix(m, "H607"):
		return true
	}
	return false
}

// IsRGBIC reports multi-zone strips that understand BLE 0x15 / LAN ptReal
// segment writes. Floor lamps and bulbs are not RGBIC.
func IsRGBIC(model string) bool {
	m := strings.ToUpper(strings.TrimSpace(model))
	if IsClassicBulb(m) {
		return false
	}
	switch {
	case strings.HasPrefix(m, "H61"),
		strings.HasPrefix(m, "H70"),
		strings.HasPrefix(m, "H80"),
		strings.HasPrefix(m, "H85"):
		return true
	}
	return false
}

// SupportsSegments is true when a strip gradient should be painted across
// zones. Single-zone bulbs always return false.
func SupportsSegments(model string) bool {
	return IsRGBIC(model)
}
