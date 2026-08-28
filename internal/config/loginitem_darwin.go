//go:build darwin

package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Login-at-start is a LaunchAgent rather than SMAppService. SMAppService wants
// a Developer ID signature and a helper bundled at build time; Lightwave is
// ad-hoc signed for local use, so a per-user agent plist is the mechanism that
// actually works here. It lives entirely in the user's own LaunchAgents
// directory — nothing is installed system-wide and no privileges are needed.

// LoginAgentLabel is the launchd job label, also the plist's filename.
const LoginAgentLabel = "com.dinksf.lightwave.login"

// LoginAgentPath is where the agent plist lives for the current user.
func LoginAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", LoginAgentLabel+".plist"), nil
}

// appBundlePath returns the .app to launch at login. launchd starts a bare
// executable without the bundle around it, which on macOS means no Dock entry,
// no menu bar, and a process that cannot show a window — so the bundle is what
// must be launched, not os.Executable() directly.
func appBundlePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// .../Lightwave.app/Contents/MacOS/lightwave -> .../Lightwave.app
	dir := filepath.Dir(exe)
	if filepath.Base(dir) == "MacOS" {
		contents := filepath.Dir(dir)
		if filepath.Base(contents) == "Contents" {
			bundle := filepath.Dir(contents)
			if strings.HasSuffix(bundle, ".app") {
				return bundle, nil
			}
		}
	}
	return "", fmt.Errorf("not running from an .app bundle (%s)", exe)
}

// LoginItemEnabled reports whether the agent plist is installed.
func LoginItemEnabled() bool {
	p, err := LoginAgentPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// SetLoginItem installs or removes the LaunchAgent. Enabling writes a plist
// that opens the app bundle with --hidden, so a login start comes up in the
// background: the lights, MIDI, and the Stream Deck socket are live without a
// window stealing focus on every boot.
func SetLoginItem(on bool) error {
	path, err := LoginAgentPath()
	if err != nil {
		return err
	}
	if !on {
		// Unload before removing, so the job does not linger until next login.
		_ = exec.Command("launchctl", "bootout", "gui/"+uid()+"/"+LoginAgentLabel).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	bundle, err := appBundlePath()
	if err != nil {
		return fmt.Errorf("launch at login needs the built app: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// `open -a <bundle> --args --hidden` rather than exec'ing the binary:
	// going through open keeps the process a proper GUI app, and honours the
	// single-instance socket if Lightwave is somehow already running.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LoginAgentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/open</string>
		<string>-a</string>
		<string>` + xmlEscape(bundle) + `</string>
		<string>--args</string>
		<string>--hidden</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<!-- Start once at login and stay out of the way: without this launchd
	     would treat the short-lived open command as a crash and respawn it. -->
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	// Register it now so the setting takes effect without a logout. A failure
	// here is not fatal: the plist is on disk and will load at next login.
	_ = exec.Command("launchctl", "bootout", "gui/"+uid()+"/"+LoginAgentLabel).Run()
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid(), path).CombinedOutput(); err != nil {
		return fmt.Errorf("registered for next login, but launchctl said: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func uid() string { return fmt.Sprint(os.Getuid()) }

// xmlEscape guards against a path containing plist-breaking characters.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
