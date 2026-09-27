//go:build !darwin

package web

// advertise is a no-op off macOS; the iOS app falls back to a typed address.
func advertise(int) func() { return func() {} }
