//go:build darwin || windows

package govee

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Must stay >= bleConnectTimeout: ensure() opens a scan and then waits
	// out the connect timeout, and for a BLE-only bulb whose UUID is not in
	// the adapter's cache, discovery is the only way it can ever be found.
	// At 10s the radio went dark for the last 5s of every connect attempt.
	bleScanWindow     = 16 * time.Second
	bleConnectTimeout = 15 * time.Second
	bleConnectKick    = 8 * time.Second // cancel a stuck connecting state
	bleKeepAlive      = 2 * time.Second
	bleQueueDepth     = 128
	bleErrLogEvery    = 30 * time.Second
	bleWriteGap       = 30 * time.Millisecond
	bleReadySettle    = 150 * time.Millisecond
	bleWriteTimeout   = 1500 * time.Millisecond
	bleSendAttempts   = 4
	bleRetryBackoff   = 120 * time.Millisecond
	bleEnqueueWait    = 750 * time.Millisecond
)

// Adapter state values. Darwin passes CoreBluetooth CBManagerState through
// unchanged; Windows maps WinRT radio status onto the same numbers so Config
// can show "Bluetooth off" / "permission needed" without a second UI path.
const (
	cbStateUnknown      = 0
	cbStateResetting    = 1
	cbStateUnsupported  = 2
	cbStateUnauthorized = 3
	cbStatePoweredOff   = 4
	cbStatePoweredOn    = 5
)

// Platform backends (ble_darwin.go / ble_windows.go) implement blePlatform*.
// They must return quickly: connect/write complete via bleOnReady / bleOnGone
// / bleOnWriteFail.

// BLE manages Govee Bluetooth peripherals: discovery by advertised name,
// lazy connections, per-device serialized writes, and the keep-alive
// heartbeat the lamps require. The radio itself lives in the platform backend.
type BLE struct {
	mu           sync.Mutex
	powered      bool
	scanning     bool
	closed       bool
	kicked       bool      // initial scan fired after power-on
	keepScan     bool      // Config is open: leave the radio scanning
	scanUntil    time.Time // latest deadline any caller asked the radio to stay up to
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
// brightness that follows it. ready/wake are driven by backend callbacks.
type bleConn struct {
	uuid    string
	queue   chan []byte
	ready   atomic.Bool
	hold    atomic.Bool   // pad ignited: keep scanning/connecting and send AA 01
	wake    chan struct{} // buffered(1); pinged on ready/gone transitions
	ack     chan bool     // write OK (true) / fail (false)
	lastErr atomic.Int64  // unix nanos of last logged failure
}

// bleActive is the manager the platform callbacks report into.
// The adapter is process-global, so there is exactly one.
var bleActive atomic.Pointer[BLE]

func NewBLE() *BLE {
	return &BLE{
		found: map[string]Device{},
		conns: map[string]*bleConn{},
		rssi:  map[string]int{},
	}
}

// Start brings up the platform radio. Cheap and non-blocking: the adapter
// reports readiness through bleOnState, which kicks the first discovery scan
// (and, on first run, the OS Bluetooth permission prompt).
func (b *BLE) Start(onDev func(Device)) error {
	b.mu.Lock()
	b.onDev = onDev
	b.mu.Unlock()
	bleActive.Store(b)
	blePlatformInit()
	return nil
}

// OnAdapter is called when the radio reports a new adapter state
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
	blePlatformScan(false)
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
	// Every caller gets a full window. Previously a later caller rode out
	// whatever was left of an earlier one, so a pad press arriving 9.9s into
	// a discovery scan got ~0.1s of radio before it went dark -- a large part
	// of why a BLE-only bulb (H6001) connected only sometimes.
	b.scanUntil = time.Now().Add(bleScanWindow)
	b.mu.Unlock()
	blePlatformHarvest()
	if already {
		return
	}
	log.Printf("govee ble: scan start")
	blePlatformScan(true)
	b.armScanStop(bleScanWindow)
}

// armScanStop closes the discovery window once no caller still wants it.
// Re-arms itself when Scan() pushed scanUntil out while the timer was pending.
func (b *BLE) armScanStop(d time.Duration) {
	time.AfterFunc(d, func() {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		if left := time.Until(b.scanUntil); left > 0 {
			b.mu.Unlock()
			b.armScanStop(left)
			return
		}
		keep := b.keepScan
		b.mu.Unlock()
		if keep {
			blePlatformHarvest()
			return
		}
		blePlatformScan(false)
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
// the backend in order. Packets are dropped while the lamp is unreachable.
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
		blePlatformWrite(c.uuid, pkt)
		if len(pkt) >= 3 && pkt[0] == 0x33 && pkt[1] == 0x01 {
			time.Sleep(80 * time.Millisecond)
		} else {
			time.Sleep(bleWriteGap)
		}
	}
	blePlatformCancel(c.uuid)
}

// ensure asks the backend for a live connection and waits for it to report
// the control characteristic in hand. Runs only on the worker.
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
	// Address may not be in the adapter cache this launch. Scan in parallel
	// so a missed cache hit still finds the lamp.
	b.Scan()
	blePlatformConnect(c.uuid)
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
			blePlatformConnect(c.uuid)
		case <-deadline.C:
			blePlatformCancel(c.uuid)
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
	blePlatformWrite(c.uuid, pkt)
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
	blePlatformScan(false)
	for _, c := range conns {
		close(c.queue) // worker cancels the connection on exit
	}
}

// StartTransport registers this manager as the package's BLE send path, so
// SendTurn/SendBrightness/SendColor route ble: addresses here.
func (b *BLE) StartTransport() {
	setBLESender(b.Send)
}

// Backend callbacks. These arrive on the radio's own thread or a WinRT
// goroutine; keep them quick and never call back into the backend synchronously.

func bleOnState(state int) {
	b := bleActive.Load()
	if b == nil {
		return
	}
	on := state == cbStatePoweredOn
	b.mu.Lock()
	b.powered = on
	b.adapterState = state
	kick := on && !b.kicked && !b.closed
	if kick {
		b.kicked = true
	}
	cb := b.onAdapter
	b.mu.Unlock()
	log.Printf("govee ble: adapter %s", cbStateName(state))
	if state == cbStateUnauthorized {
		log.Printf("govee ble: Bluetooth permission denied — enable Lightwave in system Bluetooth privacy settings")
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

func bleOnMsg(m string) {
	log.Printf("govee ble: %s", m)
}

func bleOnNeedScan(id string) {
	log.Printf("govee ble: uuid not cached, scanning %s", id)
	b := bleActive.Load()
	if b != nil {
		b.Scan()
	}
}

func bleOnWriteFail(id string) {
	log.Printf("govee ble: write failed %s", id)
	if c := bleLookup(id); c != nil {
		c.ready.Store(false)
		c.ping()
	}
}

func bleOnFound(id, name string) {
	b := bleActive.Load()
	if b == nil {
		return
	}
	id = strings.ToUpper(id)
	if !bleAcceptFound(name) {
		if bleCloseName.MatchString(name) {
			log.Printf("govee ble: skip name=%q uuid=%s", name, id)
		}
		return
	}
	model := bleModelFromName(name)
	suffix := BLESuffixFromName(name)
	d := Device{
		ID:      BLEDeviceID(id),
		Name:    BLEFallbackName(model, suffix),
		Model:   model,
		IP:      BLEPrefix + id,
		Online:  true,
		AdvName: name,
	}
	if d.Name == "Govee BLE" && name != "" && !strings.EqualFold(name, "Govee BLE") {
		d.Name = name
	}
	b.mu.Lock()
	prev, known := b.found[id]
	b.found[id] = d
	cb := b.onDev
	b.mu.Unlock()
	if !known || prev.AdvName != d.AdvName || prev.Model != d.Model {
		log.Printf("govee ble: found name=%q model=%s uuid=%s", name, model, id)
		if cb != nil {
			go cb(d)
		}
	}
	// Wake a worker waiting to connect this UUID now that the scan has it.
	if c := bleLookup(id); c != nil {
		c.ping()
	}
}

func bleOnReady(id string) {
	if c := bleLookup(id); c != nil {
		c.ready.Store(true)
		c.ping()
	}
}

func bleOnGone(id string) {
	if c := bleLookup(id); c != nil {
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
