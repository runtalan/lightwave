package main

import (
	"log"
	"sync"
	"time"

	"lightwave/internal/config"
)

// pacer runs a repaint at most once per interval, however fast it is asked
// for. A call while nothing is pending runs at once, on the caller, so a single
// press still lands before it returns. Calls that come while a run is in
// flight, or within the interval after one, fold into one trailing run that
// reads whatever state is latest by then. A slider dragged across its range
// therefore costs about ten repaints a second rather than one per event, and
// always ends on the value it stopped at.
//
// The zero value is ready to use.
type pacer struct {
	mu    sync.Mutex
	busy  bool // a run is in progress or a trailing one is scheduled
	again bool // asked for again while busy
	last  time.Time
}

func (p *pacer) kick(every time.Duration, run func()) {
	p.mu.Lock()
	if p.busy {
		p.again = true
		p.mu.Unlock()
		return
	}
	p.busy = true
	wait := every - time.Since(p.last)
	p.mu.Unlock()
	if wait > 0 {
		go p.trail(wait, every, run)
		return
	}
	run()
	if p.finish() {
		go p.trail(every, every, run)
	}
}

// finish records a run's end and reports whether another was asked for.
func (p *pacer) finish() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = time.Now()
	if !p.again {
		p.busy = false
		return false
	}
	p.again = false
	return true
}

func (p *pacer) trail(wait, every time.Duration, run func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("pacer recovered: %v", r)
			p.mu.Lock()
			p.busy, p.again = false, false
			p.mu.Unlock()
		}
	}()
	for {
		time.Sleep(wait)
		run()
		if !p.finish() {
			return
		}
		wait = every
	}
}

// settingsSaveDelay is how long settings have to rest before a slider's last
// value is written to disk. Writing on every step of a drag rewrote
// config.json dozens of times a second.
const settingsSaveDelay = 500 * time.Millisecond

// saveSettingsSoon writes a.settings once it has stopped changing for
// settingsSaveDelay. shutdown flushes a save still waiting.
func (a *App) saveSettingsSoon() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.saveTimer == nil {
		a.saveTimer = time.AfterFunc(settingsSaveDelay, a.saveSettingsNow)
		return
	}
	a.saveTimer.Reset(settingsSaveDelay)
}

func (a *App) saveSettingsNow() {
	a.mu.Lock()
	s := a.settings
	a.mu.Unlock()
	if err := config.SaveSettings(s); err != nil {
		log.Printf("persist settings: %v", err)
	}
}

// flushSettings writes a save saveSettingsSoon is still holding back.
func (a *App) flushSettings() {
	a.mu.Lock()
	pending := a.saveTimer != nil && a.saveTimer.Stop()
	a.mu.Unlock()
	if pending {
		a.saveSettingsNow()
	}
}
