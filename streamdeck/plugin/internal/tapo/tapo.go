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
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
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

	mu         sync.Mutex
	p          *proc
	user, pass string
	failedAt   time.Time
	failure    error
}

func NewClient() *Client { return &Client{} }

// SetLogin sets the TP-Link account the plugs are enrolled under. Tapo plugs on
// current firmware refuse local control without it.
func (c *Client) SetLogin(user, pass string) {
	c.mu.Lock()
	if user == c.user && pass == c.pass {
		c.mu.Unlock()
		return
	}
	c.user, c.pass = user, pass
	p := c.p
	c.mu.Unlock()
	if p != nil && p.alive() {
		go func() {
			if _, err := p.call(c.authRequest(), callTimeout); err != nil {
				log.Printf("tapo: sending login: %v", err)
			}
		}()
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
	return p.call(req, timeout)
}

// proc returns the running helper, starting it if it is not.
func (c *Client) proc() (*proc, error) {
	c.mu.Lock()
	if c.p != nil && c.p.alive() {
		p := c.p
		c.mu.Unlock()
		return p, nil
	}
	if c.failure != nil && time.Since(c.failedAt) < restartBackoff {
		err := c.failure
		c.mu.Unlock()
		return nil, err
	}
	python, helper, err := c.locate()
	if err == nil {
		c.p, err = start(python, helper)
	}
	if err != nil {
		c.failure, c.failedAt = err, time.Now()
		c.mu.Unlock()
		return nil, err
	}
	c.failure = nil
	p, user := c.p, c.user != ""
	c.mu.Unlock()

	if user {
		// The helper runs requests concurrently, so wait for the login to land
		// before the caller's first command can overtake it.
		if _, err := p.call(c.authRequest(), callTimeout); err != nil {
			log.Printf("tapo: sending login: %v", err)
		}
	}
	return p, nil
}

func (c *Client) locate() (python, helper string, err error) {
	python, helper = c.Python, c.Helper
	if helper == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		helper = filepath.Join(filepath.Dir(exe), "tapo_helper.py")
	}
	if python == "" {
		python = os.Getenv("LIGHTWAVE_TAPO_PYTHON")
	}
	if python == "" {
		python = venvPython()
	}
	for _, f := range []string{python, helper} {
		if f == "" {
			return "", "", ErrNotSetUp
		}
		if _, err := os.Stat(f); err != nil {
			return "", "", ErrNotSetUp
		}
	}
	return python, helper, nil
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

// proc is one running helper.
type proc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	wmu   sync.Mutex // one request line at a time

	mu      sync.Mutex
	pending map[int]chan reply
	nextID  int
	dead    chan struct{}
}

type reply struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func start(python, helper string) (*proc, error) {
	cmd := exec.Command(python, helper)
	hide(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Tapo helper: %w", err)
	}
	p := &proc{cmd: cmd, stdin: stdin, pending: map[int]chan reply{}, dead: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Printf("tapo helper: %s", sc.Text())
		}
	}()
	go p.read(stdout)
	return p, nil
}

func (p *proc) alive() bool {
	select {
	case <-p.dead:
		return false
	default:
		return true
	}
}

func (p *proc) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		var rep reply
		if err := json.Unmarshal(sc.Bytes(), &rep); err != nil {
			continue
		}
		p.mu.Lock()
		ch := p.pending[rep.ID]
		delete(p.pending, rep.ID)
		p.mu.Unlock()
		if ch != nil {
			ch <- rep
		}
	}
	_ = p.cmd.Wait()
	close(p.dead)
}

func (p *proc) call(req map[string]any, timeout time.Duration) (json.RawMessage, error) {
	ch := make(chan reply, 1)
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.pending[id] = ch
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}

	req["id"] = id
	line, err := json.Marshal(req)
	if err != nil {
		forget()
		return nil, err
	}
	p.wmu.Lock()
	_, err = p.stdin.Write(append(line, '\n'))
	p.wmu.Unlock()
	if err != nil {
		forget()
		return nil, fmt.Errorf("Tapo helper is not running: %w", err)
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case rep := <-ch:
		if !rep.OK {
			return nil, errors.New(strings.TrimSpace(rep.Error))
		}
		return rep.Result, nil
	case <-p.dead:
		return nil, errors.New("Tapo helper stopped; python-kasa may not be installed")
	case <-t.C:
		forget()
		return nil, errors.New("timed out waiting for the plug")
	}
}
