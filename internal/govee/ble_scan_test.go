//go:build darwin

package govee

import (
	"testing"
	"time"
)

// The discovery window must outlast a connect attempt. ensure() opens a scan
// and then waits bleConnectTimeout for the lamp; if the radio stops first, a
// BLE-only bulb whose UUID is not in CoreBluetooth's cache can never be found
// during the remainder of the wait.
func TestScanWindowOutlastsConnectTimeout(t *testing.T) {
	if bleScanWindow < bleConnectTimeout {
		t.Fatalf("scan window %v is shorter than connect timeout %v: the radio "+
			"goes dark for the last %v of every connect attempt",
			bleScanWindow, bleConnectTimeout, bleConnectTimeout-bleScanWindow)
	}
}

// A later Scan() must push the window out rather than inheriting the tail of
// an earlier one. Without this a pad press arriving near the end of a
// discovery scan got almost no radio time.
func TestScanExtendsWindowForLaterCaller(t *testing.T) {
	b := NewBLE()
	b.mu.Lock()
	b.powered = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
	}()

	b.Scan()
	b.mu.Lock()
	first := b.scanUntil
	b.mu.Unlock()

	time.Sleep(20 * time.Millisecond)
	b.Scan() // second caller: window must move forward, not stay put

	b.mu.Lock()
	second := b.scanUntil
	b.mu.Unlock()

	if !second.After(first) {
		t.Fatalf("second Scan did not extend the window: %v then %v", first, second)
	}
	if !b.Scanning() {
		t.Fatal("still expected to be scanning")
	}
}
