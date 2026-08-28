package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"strings"

	"lightwave/internal/config"
	"lightwave/internal/web"
)

// This file is the whole surface a browser can reach. The bridge in the
// frontend decides what to send, but this switch decides what is honoured, so
// a hand-written request cannot do more than the phone UI offers.
//
// Control only, deliberately. A remote client can drive the lights but cannot
// quit the app, resize or hide its window, rescan, rewrite the pad map, or
// read or change the stored Govee API key.
func (a *App) webCall(method string, args []json.RawMessage) (any, error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("web: %q recovered: %v", method, r)
		}
	}()

	intArg := func(i int) (int, error) {
		if i >= len(args) {
			return 0, fmt.Errorf("%s: missing argument %d", method, i+1)
		}
		var n int
		if err := json.Unmarshal(args[i], &n); err != nil {
			return 0, fmt.Errorf("%s: argument %d must be a number", method, i+1)
		}
		return n, nil
	}

	switch method {
	case "GetState":
		return a.webState(), nil
	case "GetSlots":
		return a.webState().Slots, nil
	case "GetActivePool":
		return a.GetActivePool(), nil
	case "GetPaletteIndex":
		return a.GetPaletteIndex(), nil
	case "Dancing":
		return a.Dancing(), nil

	case "ToggleSlot":
		n, err := intArg(0)
		if err != nil {
			return nil, err
		}
		if err := a.ToggleSlot(n); err != nil {
			return nil, err
		}
		return a.webState(), nil

	case "ToggleAll":
		a.ToggleAll()
		return a.webState(), nil
	case "AllOn":
		a.AllOn()
		return a.webState(), nil
	case "AllOff":
		a.AllOff()
		return a.webState(), nil
	case "ToggleDance":
		a.ToggleDance()
		return a.webState(), nil
	case "ToggleGradient":
		a.ToggleGradient()
		return a.webState(), nil

	case "SetBrightness":
		n, err := intArg(0)
		if err != nil {
			return nil, err
		}
		a.SetBrightness(clampBrightness(n))
		return a.webState(), nil

	case "CycleColor":
		n, err := intArg(0)
		if err != nil {
			return nil, err
		}
		a.CycleColor(n)
		return a.webState(), nil

	case "SetPalette":
		n, err := intArg(0)
		if err != nil {
			return nil, err
		}
		a.SetPalette(n)
		return a.webState(), nil
	}
	return nil, fmt.Errorf("%s is not available remotely", method)
}

// webState is the snapshot sent to browsers. It is the desktop snapshot with
// two changes.
//
// Config is forced shut: the shared bundle renders Config whenever these flags
// are set, and every button in that screen is refused by webCall, so a phone
// would be looking at a dead screen. Forcing them false keeps the phone on the
// control HUD regardless of what the desktop window is showing.
//
// Settings are blanked because they carry filesystem paths and reveal whether
// an API key is stored. None of it is needed to work the lights.
func (a *App) webState() HUDState { return webStateFrom(a.snapshot()) }

func webStateFrom(st HUDState) HUDState {
	st.NeedsSetup = false
	st.SetupOpen = false
	st.ConfigOpen = false
	st.FirstRun = false
	st.Settings = SettingsView{}
	// Phone HUD never lists the catalog; dropping it shrinks every SSE push.
	st.Catalog = nil
	return st
}

// StartWebServer brings the phone control server up with the current settings.
func (a *App) startWebServer() error {
	a.mu.Lock()
	s := a.settings
	a.mu.Unlock()
	if a.webSrv == nil || a.webAssets == nil {
		return fmt.Errorf("web server is unavailable in this build")
	}
	if a.webSrv.Running() {
		return nil
	}
	if err := a.webSrv.Start(web.Options{
		Addr:   s.WebAddr,
		Token:  s.WebToken,
		Assets: a.webAssets,
		Call:   a.webCall,
	}); err != nil {
		return err
	}
	for _, u := range web.URLs(a.webSrv.Addr()) {
		log.Printf("web: reachable at %s", u)
	}
	return nil
}

// SetWebEnabled starts or stops the phone control server and remembers the
// choice. Applied immediately: a restart to pick up a toggle would be a poor
// trade for a feature whose whole point is convenience.
func (a *App) SetWebEnabled(on bool) (SettingsView, error) {
	a.mu.Lock()
	s := a.settings
	s.WebEnabled = on
	a.settings = s
	a.mu.Unlock()

	var err error
	if on {
		err = a.startWebServer()
		if err != nil {
			// Do not persist a setting that could not be honoured, or the app
			// would fail the same way on every future launch.
			a.mu.Lock()
			a.settings.WebEnabled = false
			a.mu.Unlock()
		}
	} else if a.webSrv != nil {
		err = a.webSrv.Stop()
	}
	if saveErr := config.SaveSettings(a.currentSettings()); saveErr != nil && err == nil {
		err = saveErr
	}
	a.emitState()
	return a.settingsView(), err
}

// SetLaunchAtLogin installs or removes the login agent. Enabling starts
// Lightwave hidden at login, so the lights, MIDI, and the Stream Deck socket
// are live without a window taking focus on every boot.
func (a *App) SetLaunchAtLogin(on bool) (SettingsView, error) {
	err := config.SetLoginItem(on)
	if err != nil {
		// Do not record a state the system did not accept — the toggle would
		// then disagree with what actually happens at login.
		return a.settingsView(), err
	}
	a.mu.Lock()
	a.settings.LaunchAtLogin = on
	a.mu.Unlock()
	if saveErr := config.SaveSettings(a.currentSettings()); saveErr != nil {
		err = saveErr
	}
	a.emitState()
	return a.settingsView(), err
}

// SetWebConfig updates the listen address and token, restarting the server if
// it is running so the change takes effect at once.
func (a *App) SetWebConfig(addr, token string) (SettingsView, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = config.DefaultWebAddr
	}
	a.mu.Lock()
	s := a.settings
	s.WebAddr = addr
	s.WebToken = strings.TrimSpace(token)
	a.settings = s
	running := s.WebEnabled
	a.mu.Unlock()

	if err := config.SaveSettings(a.currentSettings()); err != nil {
		return a.settingsView(), err
	}
	if running && a.webSrv != nil {
		if err := a.webSrv.Stop(); err != nil {
			return a.settingsView(), err
		}
		if err := a.startWebServer(); err != nil {
			return a.settingsView(), err
		}
	}
	a.emitState()
	return a.settingsView(), nil
}

func (a *App) currentSettings() config.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

func (a *App) settingsView() SettingsView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settingsViewLocked()
}

// SetWebAssets hands the app the embedded frontend and creates the server.
// Called from main before startup; the server itself binds nothing until it is
// switched on.
func (a *App) SetWebAssets(dist fs.FS) {
	a.webAssets = dist
	a.webSrv = web.New()
}
