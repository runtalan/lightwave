//go:build windows

package discovery

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/saltosystems/winrt-go"
	"github.com/saltosystems/winrt-go/windows/devices/bluetooth/advertisement"
	"github.com/saltosystems/winrt-go/windows/foundation"
	"golang.org/x/sys/windows"
)

// Each scan owns its watcher; late WinRT callbacks cannot mutate the catalog.
func ScanBluetooth(ctx context.Context, found func(BluetoothDevice), status func(string)) {
	if err := scanWindows(ctx, found, status); err != nil {
		status("Bluetooth discovery failed. Check the radio and permission: " + err.Error())
	}
}

func scanWindows(ctx context.Context, found func(BluetoothDevice), status func(string)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.RoInitialize(1); err != nil {
		return err
	}
	defer windows.NewLazySystemDLL("combase.dll").NewProc("RoUninitialize").Call()
	w, err := advertisement.NewBluetoothLEAdvertisementWatcher()
	if err != nil {
		return err
	}
	defer w.Release()
	if err := w.SetScanningMode(advertisement.BluetoothLEScanningModeActive); err != nil {
		return err
	}
	results := make(chan BluetoothDevice, 128)
	stopped := make(chan error, 1)
	receivedGUID := winrt.ParameterizedInstanceGUID(foundation.GUIDTypedEventHandler,
		advertisement.SignatureBluetoothLEAdvertisementWatcher, advertisement.SignatureBluetoothLEAdvertisementReceivedEventArgs)
	received := foundation.NewTypedEventHandler(ole.NewGUID(receivedGUID), func(_ *foundation.TypedEventHandler, _, arg unsafe.Pointer) {
		if d, ok := windowsAdvertisement((*advertisement.BluetoothLEAdvertisementReceivedEventArgs)(arg)); ok {
			select {
			case results <- d:
			default:
			}
		}
	})
	defer received.Release()
	token, err := w.AddReceived(received)
	if err != nil {
		return err
	}
	defer w.RemoveReceived(token)
	stoppedGUID := winrt.ParameterizedInstanceGUID(foundation.GUIDTypedEventHandler,
		advertisement.SignatureBluetoothLEAdvertisementWatcher, advertisement.SignatureBluetoothLEAdvertisementWatcherStoppedEventArgs)
	onStopped := foundation.NewTypedEventHandler(ole.NewGUID(stoppedGUID), func(_ *foundation.TypedEventHandler, _, arg unsafe.Pointer) {
		code, err := (*advertisement.BluetoothLEAdvertisementWatcherStoppedEventArgs)(arg).GetError()
		if err == nil && code != 0 {
			err = fmt.Errorf("Windows Bluetooth error %d", code)
		}
		select {
		case stopped <- err:
		default:
		}
	})
	defer onStopped.Release()
	stopToken, err := w.AddStopped(onStopped)
	if err != nil {
		return err
	}
	defer w.RemoveStopped(stopToken)
	if err = w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	status("Searching for nearby Bluetooth devices…")
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	seen := map[string]BluetoothDevice{}
	for {
		select {
		case d := <-results:
			if old, ok := seen[d.ID]; ok {
				if d.Name == "Govee Bluetooth device" {
					d.Name = old.Name
				}
				if d.Model == "" {
					d.Model = old.Model
				}
				if old.Name == d.Name && old.Model == d.Model {
					continue
				}
			}
			seen[d.ID] = d
			found(d)
		case err := <-stopped:
			if err != nil {
				return err
			}
			status("Bluetooth scan stopped.")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			status("Bluetooth scan complete. Discovered devices require a separate control implementation.")
			return nil
		}
	}
}

func windowsAdvertisement(args *advertisement.BluetoothLEAdvertisementReceivedEventArgs) (BluetoothDevice, bool) {
	address, err := args.GetBluetoothAddress()
	if err != nil {
		return BluetoothDevice{}, false
	}
	adv, err := args.GetAdvertisement()
	if err != nil || adv == nil {
		return BluetoothDevice{}, false
	}
	defer adv.Release()
	name, _ := adv.GetLocalName()
	var companies []uint16
	if vector, err := adv.GetManufacturerData(); err == nil && vector != nil {
		defer vector.Release()
		size, _ := vector.GetSize()
		for i := uint32(0); i < size && i < 64; i++ {
			ptr, err := vector.GetAt(i)
			if err != nil || ptr == nil {
				continue
			}
			data := (*advertisement.BluetoothLEManufacturerData)(ptr)
			id, err := data.GetCompanyId()
			data.Release()
			if err == nil {
				companies = append(companies, id)
			}
		}
	}
	if !looksGovee(name, nil, companies) {
		return BluetoothDevice{}, false
	}
	rssi, _ := args.GetRawSignalStrengthInDBm()
	if name == "" {
		name = "Govee Bluetooth device"
	}
	return BluetoothDevice{ID: fmt.Sprintf("ble:%012X", address), Name: name, Model: modelPattern.FindString(strings.ToUpper(name)), RSSI: int(rssi), Transport: "bluetooth"}, true
}
