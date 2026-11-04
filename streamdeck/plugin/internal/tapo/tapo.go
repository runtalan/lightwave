// Package tapo drives TP-Link Tapo smart plugs (P110, P115 and kin) through
// python-kasa.
//
// The Tapo protocols — KLAP, the older AES handshake, the account login behind
// both — are python-kasa's to get right, so the plugin does not reimplement
// them. Instead it runs tapo_helper.py as a child process and exchanges one JSON
// line per request and per reply with it. The process starts on first use, so a
// deck with no Tapo keys never launches Python.
package tapo

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"lightwave-sd/internal/helper"
)

// Reading is one plug's state at a moment.
type Reading struct {
	On bool `json:"on"`
	// Watts is nil for a plug that does not report power (a P100, say).
	Watts *float64 `json:"watts"`
	Alias string   `json:"alias"`
}

// Plug is a plug found by discovery.
type Plug struct {
	Host  string `json:"host"`
	Alias string `json:"alias"`
	Model string `json:"model"`
	// Auth is false when the plug answered but would not talk without a
	// (correct) TP-Link login, so its name could not be read.
	Auth bool `json:"auth"`
}

// Timeouts. A plug on the LAN answers in well under a second; these only bound
// how long a key waits on one that has gone away.
const (
	callTimeout     = 20 * time.Second
	discoverTimeout = 30 * time.Second
	// A helper that fails to start is not retried more often than this, so a
	// missing Python does not turn every poll into a process spawn.
	restartBackoff = 10 * time.Second
)

// ErrNotSetUp means there is no Python environment with python-kasa for the
// plugin to use.
var ErrNotSetUp = errors.New("Tapo support is not set up; run streamdeck/setup-tapo.sh")

// Client talks to one helper process, starting and restarting it as needed.
type Client struct {
	// Python and Helper override where the interpreter and script are found.
	// Empty means the installed locations; tests set them.
	Python, Helper string

	mu sync.Mutex
	// startMu is held while a helper starts and takes its login, so no
	// command reaches a helper that does not have the login yet.
	startMu    sync.Mutex
	p          *helper.Proc
	user, pass string
	failedAt   time.Time
	failure    error
}

func NewClient() *Client { return &Client{} }

// SetLogin sets the TP-Link account the plugs are enrolled under. Tapo plugs on
// current firmware refuse local control without it. It reports whether the
// login changed.
func (c *Client) SetLogin(user, pass string) bool {
	c.mu.Lock()
	if user == c.user && pass == c.pass {
		c.mu.Unlock()
		return false
	}
	c.user, c.pass = user, pass
	p := c.p
	c.mu.Unlock()
	if p != nil && p.Alive() {
		go func() {
			if _, err := p.Call(c.authRequest(), callTimeout); err != nil {
				log.Printf("tapo: sending login: %v", err)
			}
		}()
	}
	return true
}

// HasLogin reports whether an account has been set.
func (c *Client) HasLogin() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.user != "" && c.pass != ""
}

func (c *Client) authRequest() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{"cmd": "auth", "username": c.user, "password": c.pass}
}

// State reads a plug.
func (c *Client) State(host string) (Reading, error) {
	return c.reading(map[string]any{"cmd": "state", "host": host})
}

// Set switches a plug on or off and returns how it ended up.
func (c *Client) Set(host string, on bool) (Reading, error) {
	return c.reading(map[string]any{"cmd": "set", "host": host, "on": on})
}

// Toggle flips a plug and returns how it ended up. The flip happens in the
// helper, from the plug's own current state, so two quick presses cannot both
// read "off" and both turn it on.
func (c *Client) Toggle(host string) (Reading, error) {
	return c.reading(map[string]any{"cmd": "toggle", "host": host})
}

// Discover finds plugs on the local network. It takes several seconds.
func (c *Client) Discover() ([]Plug, error) {
	raw, err := c.call(map[string]any{"cmd": "discover"}, discoverTimeout)
	if err != nil {
		return nil, err
	}
	var out struct {
		Plugs []Plug `json:"plugs"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Plugs, nil
}

func (c *Client) reading(req map[string]any) (Reading, error) {
	raw, err := c.call(req, callTimeout)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	if err := json.Unmarshal(raw, &r); err != nil {
		return Reading{}, err
	}
	return r, nil
}

func (c *Client) call(req map[string]any, timeout time.Duration) (json.RawMessage, error) {
	p, err := c.proc()
	if err != nil {
		return nil, err
	}
	return p.Call(req, timeout)
}

// proc returns the running helper, starting it if it is not.
func (c *Client) proc() (*helper.Proc, error) {
	if p := c.running(); p != nil {
		return p, nil
	}
	// Callers that arrive while the helper starts wait here for it, rather than
	// sending their commands ahead of the login. A plug asked without the login
	// rejects it, and that holds the plug off for a minute.
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if p := c.running(); p != nil {
		return p, nil
	}

	c.mu.Lock()
	if c.failure != nil && time.Since(c.failedAt) < restartBackoff {
		err := c.failure
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	python, helperPath, err := c.locate()
	var p *helper.Proc
	if err == nil {
		p, err = helper.Start("Tapo", python, helperPath)
	}
	if err != nil {
		c.mu.Lock()
		c.failure, c.failedAt = err, time.Now()
		c.mu.Unlock()
		return nil, err
	}

	// The helper is published only once it has the current login. SetLogin
	// cannot reach it before then, so send again if the login changed meanwhile.
	sentUser, sentPass := "", ""
	for {
		c.mu.Lock()
		user, pass := c.user, c.pass
		if user == sentUser && pass == sentPass {
			c.p, c.failure = p, nil
			c.mu.Unlock()
			return p, nil
		}
		c.mu.Unlock()
		if _, err := p.Call(map[string]any{"cmd": "auth", "username": user, "password": pass}, callTimeout); err != nil {
			log.Printf("tapo: sending login: %v", err)
		}
		sentUser, sentPass = user, pass
	}
}

// running is the helper if it is up.
func (c *Client) running() *helper.Proc {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p != nil && c.p.Alive() {
		return c.p
	}
	return nil
}

func (c *Client) locate() (python, script string, err error) {
	python, script = c.Python, c.Helper
	if script == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		script = filepath.Join(filepath.Dir(exe), "tapo_helper.py")
	}
	if python == "" {
		python = os.Getenv("LIGHTWAVE_TAPO_PYTHON")
	}
	if python == "" {
		python = venvPython()
	}
	for _, f := range []string{python, script} {
		if f == "" {
			return "", "", ErrNotSetUp
		}
		if _, err := os.Stat(f); err != nil {
			return "", "", ErrNotSetUp
		}
	}
	return python, script, nil
}

// VenvDir is where setup-tapo puts the Python environment: next to Lightwave's
// own data, so reinstalling the plugin does not throw it away.
func VenvDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "Lightwave", "tapo-venv")
}

func venvPython() string {
	dir := VenvDir()
	if dir == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python")
}
