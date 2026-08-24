//go:build !darwin

package config

import "errors"

// Launch-at-login is implemented with a macOS LaunchAgent; other platforms
// would need their own mechanism, so the setting is simply unavailable.

const LoginAgentLabel = "com.dinksf.lightwave.login"

func LoginAgentPath() (string, error) { return "", errors.New("unsupported on this platform") }

func LoginItemEnabled() bool { return false }

func SetLoginItem(bool) error { return errors.New("launch at login is only supported on macOS") }
