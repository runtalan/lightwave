// Package dreame drives a Dreame robot vacuum through the Dreamehome cloud.
//
// Dreame publishes no API, so the client is Tasshack/dreame-vacuum's protocol
// module (MIT), which setup-dreame.sh fetches at a pinned commit. It is Python,
// so as with Tapo the plugin runs dreame_helper.py as a child process and
// exchanges one JSON line per request and reply. The process starts on first
// use: a deck with no vacuum key never launches it.
package dreame

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"lightwave-sd/internal/helper"
)

// State is what the vacuum last reported.
type State struct {
	// Label is one short word for a key: CLEANING, PAUSED, DOCKED, ...
	Label string `json:"label"`
	// Active is true while a press should pause rather than start.
	Active bool `json:"active"`
	// Battery is a percentage; nil when the vacuum did not say.
	Battery *int `json:"battery"`
	// Minutes and Area are what the current (or last) run has cleaned.
	Minutes *int `json:"minutes"`
	Area    *int `json:"area"`
	// Error is the vacuum's fault code, nil while there is none.
	Error *int `json:"error"`
}

// Vacuum is one vacuum on the account.
type Vacuum struct {
	DID    string `json:"did"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Online bool   `json:"online"`
}

// Commands Do accepts.
const (
	Toggle = "toggle" // pause if cleaning, otherwise start or resume
	Start  = "start"
	Pause  = "pause"
	Dock   = "dock"
)

const (
	// The cloud is slower than a LAN plug, and a command waits a moment for
	// the vacuum to react before reporting.
	callTimeout    = 40 * time.Second
	restartBackoff = 10 * time.Second
)

// ErrNotSetUp means there is no Python environment with the Dreame client.
var ErrNotSetUp = errors.New("Dreame support is not set up; run streamdeck/setup-dreame.sh")

// Client talks to one helper process, starting and restarting it as needed.
type Client struct {
	// Python and Helper override where the interpreter and script are found.
	// Empty means the installed locations; tests set them.
	Python, Helper string

	mu                  sync.Mutex
	p                   *helper.Proc
	user, pass, country string
	failedAt            time.Time
	failure             error
}

func NewClient() *Client { return &Client{} }

// SetLogin sets the Dreamehome account and region. Region is where the account
// was created: us, eu, cn, sg, ru, and so on.
func (c *Client) SetLogin(user, pass, country string) {
	if country == "" {
		country = "us"
	}
	c.mu.Lock()
	if user == c.user && pass == c.pass && country == c.country {
		c.mu.Unlock()
		return
	}
	c.user, c.pass, c.country = user, pass, country
	p := c.p
	c.mu.Unlock()
	if p != nil && p.Alive() {
		go func() { _, _ = p.Call(c.authRequest(), callTimeout) }()
	}
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
	return map[string]any{"cmd": "auth", "username": c.user, "password": c.pass, "country": c.country}
}

// State reads the vacuum.
func (c *Client) State(did string) (State, error) {
	return c.state(map[string]any{"cmd": "state", "did": did})
}

// Do sends a command and returns the vacuum's state afterwards.
func (c *Client) Do(did, command string) (State, error) {
	return c.state(map[string]any{"cmd": command, "did": did})
}

// DoRoom cleans one room, by the number the vacuum's map gives it, or pauses the
// vacuum if it is already working. repeat is how many passes, 1 to 3.
func (c *Client) DoRoom(did string, room, repeat int) (State, error) {
	return c.state(map[string]any{"cmd": "room", "did": did, "room": room, "repeat": repeat})
}

// Vacuums lists the vacuums on the account.
func (c *Client) Vacuums() ([]Vacuum, error) {
	raw, err := c.call(map[string]any{"cmd": "devices"})
	if err != nil {
		return nil, err
	}
	var out struct {
		Vacuums []Vacuum `json:"vacuums"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Vacuums, nil
}

func (c *Client) state(req map[string]any) (State, error) {
	raw, err := c.call(req)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, err
	}
	return s, nil
}

func (c *Client) call(req map[string]any) (json.RawMessage, error) {
	p, err := c.proc()
	if err != nil {
		return nil, err
	}
	return p.Call(req, callTimeout)
}

// proc returns the running helper, starting it if it is not.
func (c *Client) proc() (*helper.Proc, error) {
	c.mu.Lock()
	if c.p != nil && c.p.Alive() {
		p := c.p
		c.mu.Unlock()
		return p, nil
	}
	if c.failure != nil && time.Since(c.failedAt) < restartBackoff {
		err := c.failure
		c.mu.Unlock()
		return nil, err
	}
	python, script, err := c.locate()
	if err == nil {
		c.p, err = helper.Start("Dreame", python, script)
	}
	if err != nil {
		c.failure, c.failedAt = err, time.Now()
		c.mu.Unlock()
		return nil, err
	}
	c.failure = nil
	p, haveLogin := c.p, c.user != ""
	c.mu.Unlock()

	if haveLogin {
		// Requests run concurrently in the helper, so the login has to land
		// before the caller's first command can overtake it.
		_, _ = p.Call(c.authRequest(), callTimeout)
	}
	return p, nil
}

func (c *Client) locate() (python, script string, err error) {
	python, script = c.Python, c.Helper
	if script == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		script = filepath.Join(filepath.Dir(exe), "dreame_helper.py")
	}
	if python == "" {
		python = os.Getenv("LIGHTWAVE_DREAME_PYTHON")
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

// BaseDir is where setup-dreame puts the Python environment and the vendored
// client, next to Lightwave's own data so reinstalling the plugin keeps them.
func BaseDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "Lightwave", "dreame")
}

func venvPython() string {
	dir := BaseDir()
	if dir == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(dir, "venv", "bin", "python")
}
