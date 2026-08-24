package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"lightwave/internal/config"
	"lightwave/internal/ipc"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	config.LoadEnv()

	cmd := "SHOW"
	forceSetup := false
	hidden := false
	for _, arg := range os.Args[1:] {
		switch strings.TrimSpace(arg) {
		case "--hidden":
			// Start in the background: lights, MIDI, and the Stream Deck
			// socket come up, but no window takes focus. This is how the
			// login agent starts Lightwave.
			hidden = true
			cmd = "NONE"
		case "--toggle":
			cmd = "TOGGLE"
		case "--setup", "--config":
			cmd = "SETUP"
			forceSetup = true
		case "--help", "-h":
			printHelp()
			return
		}
	}

	app := NewApp(forceSetup)
	// The phone server serves the same embedded bundle the window runs, so
	// hosting it adds no assets to the process.
	if dist, err := fs.Sub(assets, "frontend/dist"); err == nil {
		app.SetWebAssets(dist)
	} else {
		fmt.Println("web: assets unavailable:", err.Error())
	}
	width, height := HUDW, HUDH
	if app.IsSetupOpen() {
		width, height = ConfigW, ConfigH
	}

	primary, srv, err := ipc.DialOrServe(cmd, func(c string) {
		app.HandleIPC(c)
	})
	if err != nil {
		fmt.Println("ipc:", err.Error())
		os.Exit(1)
	}
	if !primary {
		return
	}
	defer srv.Close()
	// Hand the socket to the app so external controllers (the Stream Deck
	// plugin) can drive lights and subscribe to state.
	app.SetIPCServer(srv)

	err = wails.Run(&options.App{
		Title:             "Lightwave",
		Width:             width,
		Height:            height,
		MinWidth:          WindowMinW,
		MinHeight:         WindowMinH,
		StartHidden:       hidden,
		Frameless:         true,
		AlwaysOnTop:       true,
		DisableResize:     true,
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 10, G: 6, B: 18, A: 255},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: app.startup,
		OnDomReady: func(ctx context.Context) {
			app.MarkUIReady()
		},
		OnShutdown: app.shutdown,
		Menu:       appMenu(app),
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			Appearance:           mac.NSAppearanceNameDarkAqua,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "Lightwave",
				Message: "Local Govee lighting control center",
			},
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}

func printHelp() {
	fmt.Print(`Lightwave — local Govee lighting HUD

Usage:
  lightwave            Start (or focus) the HUD
  lightwave --hidden   Start in the background, no window (login agent)
  lightwave --toggle   Show/hide the HUD (Stream Deck)
  lightwave --setup    Open Config (Lights tab)
  lightwave --config   Same as --setup
  lightwave --help     This text

Phone control (Config -> Remote): serves the same HUD over HTTP to
devices on your LAN or VPN. Off by default; public addresses are always
refused.

Single-instance: a second launch signals /tmp/lightwave.sock and exits.

Env (see .env.example):
  GOVEE_API_KEY     Govee Developer Cloud key (discovery only)
  MIDI_CC           Brightness CC (default 7)
  MIDI_CC_ALT       Alternate brightness CC (default 1)
  MIDI_NOTE_PLUS    Color engine + note (default 61; 61 always +)
  MIDI_NOTE_MINUS   Color engine - note (default 60; 60 always −)
`)
}
