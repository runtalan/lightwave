//go:build !darwin && !windows

package config

import "errors"

// Launch-at-login is implemented per OS (LaunchAgent on macOS, HKCU Run on
// Windows). Platforms without a backend keep the setting unavailable.

func LoginAgentPath() (string, error) { return "", errors.New("unsupported on this platform") }

func LoginItemEnabled() bool { return false }

func SetLoginItem(bool) error { return errors.New("launch at login is not supported on this platform") }
