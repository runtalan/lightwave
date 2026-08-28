package main

import (
	"log"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
)

// appMenu builds the application menu.
//
// Only the App, Edit and Window roles are implemented in Wails v2.15 —
// FileMenuRole, MinimizeRole and QuitRole are all commented out upstream — and
// WindowMenu's items are documented as inert on a frameless window, which this
// one is. So File is assembled from explicit items bound to the app's own
// methods, which work regardless of the window chrome.
//
// The accelerators mirror the shortcuts the HUD already answers to, so the menu
// documents the keyboard rather than competing with it. File > Save runs the
// same PersistNow the HUD's Cmd-S uses; while Config is open the frontend's
// capture-phase handler takes the keystroke first (it has a settings draft to
// flush), so the two never both fire for one press.
func appMenu(app *App) *menu.Menu {
	m := menu.NewMenu()
	m.Append(menu.AppMenu())

	file := m.AddSubmenu("File")
	// No accelerator: the frontend already binds Cmd-S on both screens, and it
	// does more than this item can (Config flushes its settings draft first,
	// the HUD flashes a saved/failed toast). Claiming the key here would let
	// the menu swallow the keystroke before the webview sees it. The item
	// stays so Save is discoverable; the shortcut is shown by the HUD itself.
	file.AddText("Save", nil, func(_ *menu.CallbackData) {
		if err := app.PersistNow(); err != nil {
			logMenuErr("save", err)
		}
	})
	file.AddSeparator()
	file.AddText("Settings…", keys.CmdOrCtrl(","), func(_ *menu.CallbackData) {
		app.OpenConfig()
	})
	file.AddSeparator()
	// Minimize matches the title bar button and the Enter keycap: it hides the
	// HUD without quitting, which is this app's idea of minimising.
	file.AddText("Minimize", keys.CmdOrCtrl("m"), func(_ *menu.CallbackData) {
		app.HideHUD()
	})
	file.AddText("Quit Lightwave", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) {
		app.Quit()
	})

	// Edit gives the config panes working Cut/Copy/Paste and Select All in
	// their text fields; without it the standard shortcuts do nothing.
	m.Append(menu.EditMenu())

	return m
}

func logMenuErr(what string, err error) {
	log.Printf("menu %s: %v", what, err)
}
