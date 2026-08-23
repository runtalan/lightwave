//go:build darwin

package govee

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework CoreBluetooth

#include <stdlib.h>

void lw_ble_init(void);
void lw_ble_scan(int on);
void lw_ble_connect(const char *uuid);
void lw_ble_cancel(const char *uuid);
void lw_ble_write(const char *uuid, const void *buf, int len);
*/
import "C"

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	bleScanWindow     = 10 * time.Second
	bleConnectTimeout = 8 * time.Second
	bleKeepAlive      = 2 * time.Second
	bleQueueDepth     = 64
	bleErrLogEvery    = 30 * time.Second
)

// BLE manages Govee Bluetooth peripherals through the CoreBluetooth bridge in
// ble_darwin.m: discovery by advertised name, lazy connections, per-device
// serialized writes, and the keep-alive heartbeat the lamps require.
type BLE struct {
	mu       sync.Mutex
	powered  bool
	scanning bool
	closed   bool
	kicked   bool // initial scan fired after power-on
	onDev    func(Device)
	found    map[string]Device
	conns    map[string]*bleConn
}

// bleConn is one peripheral's write pipeline. A single worker goroutine owns
// the queue and drains it in order, so a turn-on always lands before the
// brightness that follows it. ready/wake are driven by delegate callbacks.
type bleConn struct {
	uuid    string
	queue   chan []byte
	ready   atomic.Bool
	wake    chan struct{} // buffered(1); pinged on ready/gone transitions
	lastErr atomic.Int64  // unix nanos of last logged failure
}

// bleActive is the manager the exported delegate callbacks report into.
// CoreBluetooth is process-global, so there is exactly one.
var bleActive atomic.Pointer[BLE]

func NewBLE() *BLE {
	return &BLE{
		found: map[string]Device{},
		conns: map[string]*bleConn{},
	}
}

// Start brings up CoreBluetooth. Cheap and non-blocking: the adapter reports
// readiness through goBLEState, which kicks the first discovery scan (and, on
// first ever run, macOS shows the Bluetooth permission prompt).
func (b *BLE) Start(onDev func(Device)) error {
	b.mu.Lock()
	b.onDev = onDev
	b.mu.Unlock()
	bleActive.Store(b)
	C.lw_ble_init()
	return nil
}

// Scan opens one discovery window. No-op while the adapter is off or a
// window is already open.
func (b *BLE) Scan() {
	b.mu.Lock()
	if b.closed || !b.powered || b.scanning {
		b.mu.Unlock()
		return
	}
	b.scanning = true
	b.mu.Unlock()
	C.lw_ble_scan(1)
	time.AfterFunc(bleScanWindow, func() {
		C.lw_ble_scan(0)
		b.mu.Lock()
		b.scanning = false
		b.mu.Unlock()
	})
}

// Devices lists every Govee peripheral seen by any scan this run.
func (b *BLE) Devices() []Device {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Device, 0, len(b.found))
	for _, d := range b.found {
		out = append(out, d)
	}
	return out
}

// Send queues one packet for a ble: address. Non-blocking, like the UDP path:
// connection setup and retries happen on the device's worker goroutine.
func (b *BLE) Send(addr string, pkt []byte) error {
	uuid := BLEAddrUUID(addr)
	if uuid == "" {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errShuttingDown
	}
	c, ok := b.conns[uuid]
	if !ok {
		c = &bleConn{uuid: uuid, queue: make(chan []byte, bleQueueDepth), wake: make(chan struct{}, 1)}
		b.conns[uuid] = c
		go b.worker(c)
	}
	b.mu.Unlock()
	select {
	case c.queue <- pkt:
	default:
		// Queue full means the peripheral is unreachable or drowning; the
		// pump upstream already rate-limits, so dropping is the right move.
	}
	return nil
}

// worker owns one peripheral's queue: connect on demand, then hand packets to
// the bridge in order. Packets are dropped while the lamp is unreachable.
func (b *BLE) worker(c *bleConn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("govee ble: worker %s recovered: %v", c.uuid, r)
		}
	}()
	for pkt := range c.queue {
		if !b.ensure(c) {
			continue
		}
		cu := C.CString(c.uuid)
		C.lw_ble_write(cu, unsafe.Pointer(&pkt[0]), C.int(len(pkt)))
		C.free(unsafe.Pointer(cu))
	}
	cu := C.CString(c.uuid)
	C.lw_ble_cancel(cu)
	C.free(unsafe.Pointer(cu))
}

// ensure asks the bridge for a live connection and waits for the delegate to
// report the control characteristic in hand. Runs only on the worker.
func (b *BLE) ensure(c *bleConn) bool {
	if c.ready.Load() {
		return true
	}
	b.mu.Lock()
	powered, closed := b.powered, b.closed
	b.mu.Unlock()
	if !powered || closed {
		return false
	}
	// Drain a stale wake ping so the wait below only sees fresh events.
	select {
	case <-c.wake:
	default:
	}
	cu := C.CString(c.uuid)
	C.lw_ble_connect(cu)
	C.free(unsafe.Pointer(cu))
	deadline := time.NewTimer(bleConnectTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-c.wake:
			if c.ready.Load() {
				log.Printf("govee ble: connected %s", c.uuid)
				return true
			}
			c.logErr("connect refused")
			return false
		case <-deadline.C:
			cu := C.CString(c.uuid)
			C.lw_ble_cancel(cu)
			C.free(unsafe.Pointer(cu))
			c.logErr("connect timeout")
			return false
		}
	}
}

func (c *bleConn) ping() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *bleConn) logErr(what string) {
	now := time.Now().UnixNano()
	last := c.lastErr.Load()
	if now-last < int64(bleErrLogEvery) {
		return
	}
	if c.lastErr.CompareAndSwap(last, now) {
		log.Printf("govee ble: %s: %s", what, c.uuid)
	}
}

// keepAliveLoop feeds every live connection the heartbeat Govee lamps expect;
// without it they close the link after a few seconds of silence.
func (b *BLE) keepAliveLoop() {
	t := time.NewTicker(bleKeepAlive)
	defer t.Stop()
	for range t.C {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		conns := make([]*bleConn, 0, len(b.conns))
		for _, c := range b.conns {
			conns = append(conns, c)
		}
		b.mu.Unlock()
		pkt := blePacketKeepAlive()
		for _, c := range conns {
			if !c.ready.Load() {
				continue
			}
			select {
			case c.queue <- pkt:
			default:
			}
		}
	}
}

func (b *BLE) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	conns := b.conns
	b.conns = map[string]*bleConn{}
	b.mu.Unlock()
	setBLESender(nil)
	bleActive.CompareAndSwap(b, nil)
	C.lw_ble_scan(0)
	for _, c := range conns {
		close(c.queue) // worker cancels the connection on exit
	}
}

// StartTransport registers this manager as the package's BLE send path, so
// SendTurn/SendBrightness/SendColor route ble: addresses here.
func (b *BLE) StartTransport() {
	setBLESender(b.Send)
}

// Delegate callbacks. These arrive on the bridge's private dispatch queue;
// keep them quick and never call back into the bridge synchronously.

//export goBLEState
func goBLEState(poweredOn C.int) {
	b := bleActive.Load()
	if b == nil {
		return
	}
	on := poweredOn == 1
	b.mu.Lock()
	b.powered = on
	kick := on && !b.kicked && !b.closed
	if kick {
		b.kicked = true
	}
	b.mu.Unlock()
	log.Printf("govee ble: adapter powered=%v", on)
	if kick {
		go func() {
			b.Scan()
			b.keepAliveLoop()
		}()
	}
}

//export goBLEFound
func goBLEFound(cu, cn *C.char) {
	b := bleActive.Load()
	if b == nil {
		return
	}
	uuid, name := C.GoString(cu), C.GoString(cn)
	if !bleNameLooksGovee(name) {
		return
	}
	d := Device{
		ID:      BLEDeviceID(uuid),
		Model:   bleModelFromName(name),
		IP:      BLEPrefix + uuid,
		Online:  true,
		AdvName: name,
	}
	b.mu.Lock()
	prev, known := b.found[uuid]
	b.found[uuid] = d
	cb := b.onDev
	b.mu.Unlock()
	if cb != nil && (!known || prev.AdvName != d.AdvName) {
		log.Printf("govee ble: found %q (%s)", name, uuid)
		go cb(d)
	}
}

//export goBLEReady
func goBLEReady(cu *C.char) {
	if c := bleLookup(C.GoString(cu)); c != nil {
		c.ready.Store(true)
		c.ping()
	}
}

//export goBLEGone
func goBLEGone(cu *C.char) {
	if c := bleLookup(C.GoString(cu)); c != nil {
		c.ready.Store(false)
		c.ping()
	}
}

func bleLookup(uuid string) *bleConn {
	b := bleActive.Load()
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conns[uuid]
}
