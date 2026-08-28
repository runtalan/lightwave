//go:build windows

package govee

import (
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tinygo.org/x/bluetooth"
)

// Govee GATT: same UUIDs the CoreBluetooth bridge uses.
func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic("govee ble: " + err.Error())
	}
	return u
}

var (
	goveeSvcUUID   = mustUUID("00010203-0405-0607-0809-0a0b0c0d1910")
	goveeNtfUUID   = mustUUID("00010203-0405-0607-0809-0a0b0c0d2b10")
	goveeWriteUUID = mustUUID("00010203-0405-0607-0809-0a0b0c0d2b11")
)

const (
	gattWrite  = uint32(0x08) // GattCharacteristicPropertiesWrite
	gattNoRsp  = uint32(0x04) // GattCharacteristicPropertiesWriteWithoutResponse
	gattNotify = uint32(0x10)
)

// WinRT BLE backend. tinygo.org/x/bluetooth talks to Windows.Devices.Bluetooth
// in pure Go (no CGO), so this stays off the Wails/WebView2 UI thread — the
// same reason the Darwin side uses a private dispatch queue instead of the
// stock CoreBluetooth bindings.

var (
	winAdapter = bluetooth.DefaultAdapter
	winOnce    sync.Once
	winReady   atomic.Bool
	winScanOn  atomic.Bool
	winDevs    sync.Map // id -> *winDev
)

type winDev struct {
	mu       sync.Mutex
	id       string
	addr     bluetooth.Address
	dev      bluetooth.Device
	write    bluetooth.DeviceCharacteristic
	hasDev   bool
	hasWrite bool
	cancel   bool
	props    uint32
}

func blePlatformInit() {
	winOnce.Do(func() {
		go func() {
			// Enable (RoInitialize) on this worker, never the WebView STA.
			if err := winAdapter.Enable(); err != nil {
				log.Printf("govee ble: adapter enable: %v", err)
				bleOnState(cbStatePoweredOff)
				return
			}
			winAdapter.SetConnectHandler(func(device bluetooth.Device, connected bool) {
				id := winAddrID(device.Address)
				if connected {
					return
				}
				if d, ok := winLookup(id); ok {
					d.mu.Lock()
					d.hasDev = false
					d.hasWrite = false
					d.mu.Unlock()
				}
				bleOnGone(id)
			})
			winReady.Store(true)
			bleOnState(cbStatePoweredOn)
		}()
	})
}

func blePlatformScan(on bool) {
	if !on {
		if winScanOn.Load() {
			_ = winAdapter.StopScan()
		}
		return
	}
	if !winReady.Load() {
		return
	}
	if !winScanOn.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer winScanOn.Store(false)
		err := winAdapter.Scan(func(_ *bluetooth.Adapter, result bluetooth.ScanResult) {
			if result.AdvertisementPayload == nil {
				return
			}
			name := result.LocalName()
			var companies []uint16
			for _, md := range result.ManufacturerData() {
				companies = append(companies, md.CompanyID)
			}
			var svcs []string
			if result.HasServiceUUID(goveeSvcUUID) {
				svcs = append(svcs, goveeSvcUUID.String())
			}
			if !bleLooksGoveeAdv(name, svcs, companies) {
				return
			}
			if name == "" {
				name = "Govee BLE"
			}
			id := winAddrID(result.Address)
			d, _ := winDevs.LoadOrStore(id, &winDev{id: id, addr: result.Address})
			wd := d.(*winDev)
			wd.mu.Lock()
			wd.addr = result.Address
			wd.mu.Unlock()
			bleOnFound(id, name)
		})
		if err != nil {
			log.Printf("govee ble: scan: %v", err)
		}
	}()
}

func blePlatformHarvest() {
	// WinRT has no retrieveConnectedPeripherals equivalent that is cheap and
	// reliable from an unpackaged Win32 app. Active scanning covers H6001;
	// devices we ourselves connected stay in winDevs.
	winDevs.Range(func(key, value any) bool {
		wd := value.(*winDev)
		wd.mu.Lock()
		id := wd.id
		wd.mu.Unlock()
		// Re-announce so a worker waiting on a cached address wakes.
		if c := bleLookup(id); c != nil {
			c.ping()
		}
		return true
	})
}

func blePlatformConnect(id string) {
	go winConnect(strings.ToUpper(id))
}

func blePlatformCancel(id string) {
	id = strings.ToUpper(id)
	d, ok := winLookup(id)
	if !ok {
		return
	}
	d.mu.Lock()
	d.cancel = true
	dev := d.dev
	has := d.hasDev
	d.hasDev = false
	d.hasWrite = false
	d.mu.Unlock()
	if has {
		_ = dev.Disconnect()
	}
	bleOnGone(id)
}

func blePlatformWrite(id string, pkt []byte) {
	id = strings.ToUpper(id)
	d, ok := winLookup(id)
	if !ok {
		bleOnMsg("write missing peripheral/char " + id)
		bleOnWriteFail(id)
		return
	}
	d.mu.Lock()
	if !d.hasWrite {
		d.mu.Unlock()
		bleOnMsg("write missing peripheral/char " + id)
		bleOnWriteFail(id)
		return
	}
	ch := d.write
	props := d.props
	d.mu.Unlock()

	isKeepAlive := len(pkt) >= 2 && pkt[0] == 0xAA
	isPower := len(pkt) >= 3 && pkt[0] == 0x33 && pkt[1] == 0x01

	var err error
	switch {
	case isPower && props&gattWrite != 0:
		_, err = ch.Write(pkt)
	case props&gattNoRsp != 0:
		_, err = ch.WriteWithoutResponse(pkt)
	case props&gattWrite != 0:
		_, err = ch.Write(pkt)
	default:
		bleOnMsg("no write property " + id)
		bleOnWriteFail(id)
		return
	}
	if err != nil {
		bleOnMsg("write error " + id + ": " + err.Error())
		bleOnWriteFail(id)
		return
	}
	if !isKeepAlive {
		bleOnMsg("write " + id)
	}
}

func winConnect(id string) {
	if !winReady.Load() {
		return
	}
	d := winEnsure(id)
	d.mu.Lock()
	if d.cancel {
		d.mu.Unlock()
		return
	}
	if d.hasWrite {
		d.mu.Unlock()
		bleOnReady(id)
		return
	}
	addr := d.addr
	d.mu.Unlock()

	if winZeroAddr(addr) {
		parsed, err := winParseAddr(id)
		if err != nil {
			bleOnNeedScan(id)
			return
		}
		addr = parsed
		d.mu.Lock()
		d.addr = addr
		d.mu.Unlock()
	}

	dev, err := winAdapter.Connect(addr, bluetooth.ConnectionParams{})
	if err != nil {
		bleOnMsg("connect failed " + id + ": " + err.Error())
		d.mu.Lock()
		cancelled := d.cancel
		d.mu.Unlock()
		if !cancelled {
			bleOnGone(id)
		}
		return
	}

	d.mu.Lock()
	if d.cancel {
		d.mu.Unlock()
		_ = dev.Disconnect()
		return
	}
	d.dev = dev
	d.hasDev = true
	d.mu.Unlock()

	svcs, err := dev.DiscoverServices([]bluetooth.UUID{goveeSvcUUID})
	if err != nil || len(svcs) == 0 {
		bleOnMsg("govee service 1910 missing " + id)
		_ = dev.Disconnect()
		bleOnGone(id)
		return
	}

	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{goveeNtfUUID, goveeWriteUUID})
	if err != nil {
		// Some SKUs only expose 2b11. Discover everything and pick it out.
		chars, err = svcs[0].DiscoverCharacteristics(nil)
		if err != nil {
			bleOnMsg("chars error " + id + ": " + err.Error())
			_ = dev.Disconnect()
			bleOnGone(id)
			return
		}
	}

	var writeCh bluetooth.DeviceCharacteristic
	var ntfCh bluetooth.DeviceCharacteristic
	var haveWrite, haveNtf bool
	for _, c := range chars {
		switch {
		case c.UUID() == goveeWriteUUID:
			writeCh, haveWrite = c, true
		case c.UUID() == goveeNtfUUID:
			ntfCh, haveNtf = c, true
		}
	}
	if !haveWrite {
		bleOnMsg("write characteristic 2b11 missing " + id)
		_ = dev.Disconnect()
		bleOnGone(id)
		return
	}

	if haveNtf {
		// H6001 ignores writes to 2b11 until notifications are enabled on 2b10.
		_ = ntfCh.EnableNotifications(func(buf []byte) {
			if len(buf) == 0 {
				return
			}
			bleOnMsg("notify rx " + id + ": " + hex.EncodeToString(buf))
		})
	}

	d.mu.Lock()
	if d.cancel {
		d.mu.Unlock()
		_ = dev.Disconnect()
		return
	}
	d.write = writeCh
	d.hasWrite = true
	d.props = writeCh.Properties()
	d.mu.Unlock()

	if haveNtf {
		// Notify enable is async on some firmware; don't stall the worker.
		time.AfterFunc(300*time.Millisecond, func() {
			d.mu.Lock()
			ok := d.hasWrite && !d.cancel
			d.mu.Unlock()
			if ok {
				bleOnReady(id)
			}
		})
		return
	}
	bleOnReady(id)
}

func winEnsure(id string) *winDev {
	if d, ok := winLookup(id); ok {
		d.mu.Lock()
		d.cancel = false
		d.mu.Unlock()
		return d
	}
	d := &winDev{id: id}
	if parsed, err := winParseAddr(id); err == nil {
		d.addr = parsed
	}
	actual, _ := winDevs.LoadOrStore(id, d)
	return actual.(*winDev)
}

func winLookup(id string) (*winDev, bool) {
	v, ok := winDevs.Load(strings.ToUpper(id))
	if !ok {
		return nil, false
	}
	return v.(*winDev), true
}

func winAddrID(addr bluetooth.Address) string {
	return strings.ToUpper(addr.String())
}

func winZeroAddr(addr bluetooth.Address) bool {
	for _, b := range addr.MAC {
		if b != 0 {
			return false
		}
	}
	return true
}

func winParseAddr(id string) (bluetooth.Address, error) {
	id = strings.TrimSpace(id)
	id = strings.ReplaceAll(id, "-", "")
	if !strings.Contains(id, ":") && len(id) == 12 {
		id = id[0:2] + ":" + id[2:4] + ":" + id[4:6] + ":" + id[6:8] + ":" + id[8:10] + ":" + id[10:12]
	}
	mac, err := bluetooth.ParseMAC(id)
	if err != nil {
		return bluetooth.Address{}, err
	}
	return bluetooth.Address{MACAddress: bluetooth.MACAddress{MAC: mac}}, nil
}
