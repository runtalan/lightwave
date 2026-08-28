//go:build !darwin && !windows

package govee

// BLE control is only implemented for macOS (CoreBluetooth) and Windows
// (WinRT). Other platforms get a no-op manager so the app wires up identically.
type BLE struct{}

func NewBLE() *BLE                      { return &BLE{} }
func (b *BLE) Start(func(Device)) error { return nil }
func (b *BLE) Scan()                    {}
func (b *BLE) SetKeepScanning(bool)     {}
func (b *BLE) OnAdapter(func())         {}
func (b *BLE) Scanning() bool           { return false }
func (b *BLE) Unauthorized() bool       { return false }
func (b *BLE) PoweredOff() bool         { return false }
func (b *BLE) Devices() []Device        { return nil }
func (b *BLE) Close()                   {}
func (b *BLE) StartTransport()          {}
