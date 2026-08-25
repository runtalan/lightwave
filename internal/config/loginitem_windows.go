//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const loginRunValue = "Lightwave"

func loginRunKey() string {
	return `Software\Microsoft\Windows\CurrentVersion\Run`
}

func LoginAgentPath() (string, error) {
	return loginRunKey() + `\` + loginRunValue, nil
}

func LoginItemEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, loginRunKey(), registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(loginRunValue)
	return err == nil
}

func SetLoginItem(on bool) error {
	if !on {
		k, err := registry.OpenKey(registry.CURRENT_USER, loginRunKey(), registry.SET_VALUE)
		if err != nil {
			return nil
		}
		defer k.Close()
		_ = k.DeleteValue(loginRunValue)
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// Quoted so a path with spaces still launches; --hidden keeps login from
	// stealing focus, same as the macOS LaunchAgent.
	cmd := `"` + strings.ReplaceAll(exe, `"`, `\"`) + `" --hidden`

	k, _, err := registry.CreateKey(registry.CURRENT_USER, loginRunKey(), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("start at login: %w", err)
	}
	defer k.Close()
	return k.SetStringValue(loginRunValue, cmd)
}
