//go:build darwin

package govee

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework CoreBluetooth

#include <stdlib.h>

void lw_ble_init(void);
void lw_ble_scan(int on);
void lw_ble_harvest(void);
void lw_ble_connect(const char *uuid);
void lw_ble_cancel(const char *uuid);
void lw_ble_write(const char *uuid, const void *buf, int len);
*/
import "C"

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	bleScanWindow      = 10 * time.Second
	bleConnectTimeout  = 15 * time.Second
	bleConnectKick     = 8 * time.Second // cancel a stuck CBPeripheralStateConnecting
	bleKeepAlive       = 2 * time.Second
	bleQueueDepth      = 128
	bleErrLogEvery     = 30 * time.Second
	bleWriteGap        = 30 * time.Millisecond
	bleReadySettle     = 150 * time.Millisecond
	bleWriteTimeout    = 1500 * time.Millisecond
	bleSendAttempts    = 4
	bleRetryBackoff    = 120 * time.Millisecond
	bleEnqueueWait     = 750 * time.Millisecond
)

// CBManagerState values from CoreBluetooth (passed through goBLEState).
const (
	cbStateUnknown      = 0
	cbStateResetting    = 1
	cbStateUnsupported  = 2
	cbStateUnauthorized = 3
	cbStatePoweredOff   = 4
	cbStatePoweredOn    = 5
)

// BLE manages Govee Bluetooth peripherals through the CoreBluetooth bridge in
// ble_darwin.m: discovery by advertised name, lazy connections, per-device
// serialized writes, and the keep-alive heartbeat the lamps require.
type BLE struct {
	mu           sync.Mutex
	powered      bool
	scanning     bool
	closed       bool
	kicked       bool // initial scan fired after power-on
	keepScan     bool // Config is open: leave the radio scanning
	adapterState int
	onDev        func(Device)
	onAdapter    func()
	onLink       func()
	found        map[string]Device
	conns        map[string]*bleConn
	rssi         map[string]int
}

// bleConn is one peripheral's write pipeline. A single worker goroutine owns
// the queue and drains it in order, so a turn-on always lands before the
// brightness that follows it. ready/wake are driven by delegate callbacks.
type bleConn struct {
	uuid    string
	queue   chan []byte
	ready   atomic.Bool
	hold    atomic.Bool   // pad ignited: keep scanning/connecting and send AA 01
	wake    chan struct{} // buffered(1); pinged on ready/gone transitions
	ack     chan bool     // write OK (true) / fail (false)
	lastErr atomic.Int64  // unix nanos of last logged failure
}

// bleActive is the manager the exported delegate callbacks report into.
// CoreBluetooth is process-global, so there is exactly one.
var bleActive atomic.Pointer[BLE]

func NewBLE() *BLE {
	return &BLE{
		found: map[string]Device{},
		conns: map[string]*bleConn{},
		rssi:  map[string]int{},
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

// OnAdapter is called when CoreBluetooth reports a new adapter state
// (permission, power). Config uses this to show "Bluetooth needed" immediately.
func (b *BLE) OnAdapter(f func()) {
	b.mu.Lock()
	b.onAdapter = f
	b.mu.Unlock()
}

// OnLink is called when a peripheral's weak-RSSI hint flips. HUD uses this
// for an optional "BLE weak" badge; it is not required for control.
func (b *BLE) OnLink(f func()) {
	b.mu.Lock()
	b.onLink = f
	b.mu.Unlock()
}

func (b *BLE) Scanning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.scanning
}

func (b *BLE) Unauthorized() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.adapterState == cbStateUnauthorized
}

func (b *BLE) PoweredOff() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.adapterState == cbStatePoweredOff
}

// SetKeepScanning leaves discovery running while Config is open so a slow
// advertiser (H6001) can show up after the first 10s window.
func (b *BLE) SetKeepScanning(on bool) {
	b.mu.Lock()
	b.keepScan = on
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return
	}
	if on {
		b.Scan()
		return
	}
	C.lw_ble_scan(0)
	b.mu.Lock()
	b.scanning = false
	b.mu.Unlock()
}

// Scan opens a discovery window and harvests peripherals already connected
// to the adapter. No-op while the adapter is off. If a window is already
// open, harvest still runs so a connected H6001 is not missed.
func (b *BLE) Scan() {
	b.mu.Lock()
	if b.closed || !b.powered {
		b.mu.Unlock()
		return
	}
	already := b.scanning
	b.scanning = true
	b.mu.Unlock()
	C.lw_ble_harvest()
	if already {
		return
	}
	log.Printf("govee ble: scan start")
	C.lw_ble_scan(1)
	time.AfterFunc(bleScanWindow, func() {
		b.mu.Lock()
		keep := b.keepScan && !b.closed
		b.mu.Unlock()
		if keep {
			C.lw_ble_harvest()
			return
		}
		C.lw_ble_scan(0)
		b.mu.Lock()
		b.scanning = false
		b.mu.Unlock()
		log.Printf("govee ble: scan stop")
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
	uuid := strings.ToUpper(BLEAddrUUID(addr))
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
	case c.queue <- append([]byte(nil), pkt...):
	default:
		log.Printf("govee ble: queue full, dropping %s pkt=% x", uuid, pkt)
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
		ok := b.ensure(c)
		if !ok {
			time.Sleep(500 * time.Millisecond)
			ok = b.ensure(c)
		}
		if !ok {
			log.Printf("govee ble: DROPPED %s pkt=% x", c.uuid, pkt)
			continue
		}
		if len(pkt) >= 3 && pkt[0] == 0x33 {
			log.Printf("govee ble: write %s pkt=% x", c.uuid, pkt)
		}
		cu := C.CString(c.uuid)
		C.lw_ble_write(cu, unsafe.Pointer(&pkt[0]), C.int(len(pkt)))
		C.free(unsafe.Pointer(cu))
		if len(pkt) >= 3 && pkt[0] == 0x33 && pkt[1] == 0x01 {
			time.Sleep(80 * time.Millisecond)
		} else {
			time.Sleep(bleWriteGap)
		}
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
		if !powered {
			log.Printf("govee ble: adapter not powered; cannot connect %s", c.uuid)
		}
		return false
	}
	select {
	case <-c.wake:
	default:
	}
	if c.ready.Load() {
		return true
	}
	// UUID may not be in CoreBluetooth's cache this launch. Scan in parallel
	// with retrievePeripherals so a missed cache hit still finds the lamp.
	b.Scan()
	cu := C.CString(c.uuid)
	defer C.free(unsafe.Pointer(cu))
	C.lw_ble_connect(cu)
	deadline := time.NewTimer(bleConnectTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-c.wake:
			if c.ready.Load() {
				log.Printf("govee ble: connected %s", c.uuid)
				time.Sleep(bleReadySettle)
				b.writeNow(c, blePacketKeepAlive())
				time.Sleep(bleWriteGap)
				return true
			}
			b.mu.Lock()
			powered, closed = b.powered, b.closed
			b.mu.Unlock()
			if !powered || closed {
				return false
			}
			C.lw_ble_connect(cu)
		case <-deadline.C:
			C.lw_ble_cancel(cu)
			c.logErr("connect timeout")
			log.Printf("govee ble: connect timeout %s (close the Govee app if it holds the bulb)", c.uuid)
			return false
		}
	}
}

func (b *BLE) writeNow(c *bleConn, pkt []byte) {
	if len(pkt) == 0 || !c.ready.Load() {
		return
	}
	cu := C.CString(c.uuid)
	C.lw_ble_write(cu, unsafe.Pointer(&pkt[0]), C.int(len(pkt)))
	C.free(unsafe.Pointer(cu))
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
	if last != 0 && now-last < int64(bleErrLogEvery) {
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
	b.keepScan = false
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
func goBLEState(state C.int) {
	b := bleActive.Load()
	if b == nil {
		return
	}
	on := int(state) == cbStatePoweredOn
	b.mu.Lock()
	b.powered = on
	b.adapterState = int(state)
	kick := on && !b.kicked && !b.closed
	if kick {
		b.kicked = true
	}
	cb := b.onAdapter
	b.mu.Unlock()
	log.Printf("govee ble: adapter %s", cbStateName(int(state)))
	if int(state) == cbStateUnauthorized {
		log.Printf("govee ble: Bluetooth permission denied — enable Lightwave in System Settings > Privacy & Security > Bluetooth")
	}
	if cb != nil {
		go cb()
	}
	if kick {
		go func() {
			b.Scan()
			b.keepAliveLoop()
		}()
	}
}

func cbStateName(state int) string {
	switch state {
	case cbStateUnknown:
		return "unknown"
	case cbStateResetting:
		return "resetting"
	case cbStateUnsupported:
		return "unsupported"
	case cbStateUnauthorized:
		return "unauthorized"
	case cbStatePoweredOff:
		return "powered off"
	case cbStatePoweredOn:
		return "powered on"
	}
	return "other"
}

//export goBLEMsg
func goBLEMsg(m *C.char) {
	log.Printf("govee ble: %s", C.GoString(m))
}

//export goBLENeedScan
func goBLENeedScan(cu *C.char) {
	uuid := C.GoString(cu)
	log.Printf("govee ble: uuid not cached, scanning %s", uuid)
	b := bleActive.Load()
	if b != nil {
		b.Scan()
	}
}

//export goBLEWriteFail
func goBLEWriteFail(cu *C.char) {
	uuid := C.GoString(cu)
	log.Printf("govee ble: write failed %s", uuid)
	if c := bleLookup(uuid); c != nil {
		c.ready.Store(false)
		c.ping()
	}
}

//export goBLEFound
func goBLEFound(cu, cn *C.char) {
	b := bleActive.Load()
	if b == nil {
		return
	}
	uuid, name := strings.ToUpper(C.GoString(cu)), C.GoString(cn)
	if !bleAcceptFound(name) {
		if bleCloseName.MatchString(name) {
			log.Printf("govee ble: skip name=%q uuid=%s", name, uuid)
		}
		return
	}
	model := bleModelFromName(name)
	suffix := BLESuffixFromName(name)
	d := Device{
		ID:      BLEDeviceID(uuid),
		Name:    BLEFallbackName(model, suffix),
		Model:   model,
		IP:      BLEPrefix + uuid,
		Online:  true,
		AdvName: name,
	}
	if d.Name == "Govee BLE" && name != "" && !strings.EqualFold(name, "Govee BLE") {
		d.Name = name
	}
	b.mu.Lock()
	prev, known := b.found[uuid]
	b.found[uuid] = d
	cb := b.onDev
	b.mu.Unlock()
	if !known || prev.AdvName != d.AdvName || prev.Model != d.Model {
		log.Printf("govee ble: found name=%q model=%s uuid=%s", name, model, uuid)
		if cb != nil {
			go cb(d)
		}
	}
	// Wake a worker waiting to connect this UUID now that the scan has it.
	if c := bleLookup(uuid); c != nil {
		c.ping()
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
	return b.conns[strings.ToUpper(uuid)]
}
