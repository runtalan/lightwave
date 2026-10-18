// Package helper runs a Python helper script as a child process and talks to it
// in JSON lines: one request per line out, one reply per line back, matched by
// "id". The Tapo and Dreame bridges are both this, with different scripts.
package helper

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Proc is one running helper.
type Proc struct {
	name  string
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

// Start launches script under python. name is only for logs and errors.
func Start(name, python, script string) (*Proc, error) {
	cmd := exec.Command(python, script)
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
		return nil, fmt.Errorf("start %s helper: %w", name, err)
	}
	p := &Proc{name: name, cmd: cmd, stdin: stdin, pending: map[int]chan reply{}, dead: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Printf("%s helper: %s", name, sc.Text())
		}
	}()
	go p.read(stdout)
	return p, nil
}

// Alive reports whether the process is still running.
func (p *Proc) Alive() bool {
	select {
	case <-p.dead:
		return false
	default:
		return true
	}
}

func (p *Proc) read(r io.Reader) {
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

// Call sends one request and waits for its reply. A reply that says not-ok
// comes back as an error carrying the helper's message.
func (p *Proc) Call(req map[string]any, timeout time.Duration) (json.RawMessage, error) {
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
		return nil, fmt.Errorf("%s helper is not running: %w", p.name, err)
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
		return nil, fmt.Errorf("%s helper stopped; its Python packages may not be installed", p.name)
	case <-t.C:
		forget()
		return nil, errors.New("timed out waiting for a reply")
	}
}
