//go:build !darwin && !windows

package discovery

import "context"

func ScanBluetooth(_ context.Context, _ func(BluetoothDevice), status func(string)) {
	status("Bluetooth discovery is supported on macOS and Windows only.")
}
