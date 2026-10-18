package main

import (
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"lightwave-sd/internal/dreame"
	"lightwave-sd/internal/render"
	"lightwave-sd/internal/sd"
)

// How often each vacuum on screen is re-read. The cloud is slower and more
// guarded than a LAN plug, so this is gentler than the plugs' pace.
const vacuumPoll = 10 * time.Second

// A vacuum whose login was refused is left alone this long. Repeated failed
// sign-ins to a cloud account are how accounts get locked, so this errs long;
// changing the login or the key retries at once.
const vacLoginRetry = 5 * time.Minute

// vacView is the last thing known about one vacuum.
type vacView struct {
	state   dreame.State
	have    bool
	err     error
	retryAt time.Time
}

func (p *plugin) wakeVacuums() {
	select {
	case p.vacWake <- struct{}{}:
	default:
	}
}

// clearVacBackoff lets a held-off vacuum be tried again now: all for "", or one.
func (p *plugin) clearVacBackoff(did string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for d, v := range p.vacs {
		if did == "" || d == did {
			v.retryAt = time.Time{}
			p.vacs[d] = v
		}
	}
}

// vacuumLoop re-reads every vacuum that has a key on screen. Nothing runs while
// there is none, so a deck without a vacuum key never starts the helper.
func (p *plugin) vacuumLoop() {
	t := time.NewTicker(vacuumPoll)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-p.vacWake:
		}
		p.findBarVacuum()
		for _, did := range p.vacuumIDs() {
			p.mu.Lock()
			held := time.Now().Before(p.vacs[did].retryAt)
			p.mu.Unlock()
			if held {
				continue
			}
			// One at a time: there is rarely more than one, and the helper keeps
			// a session per vacuum.
			s, err := p.dreame.State(did)
			p.setVacuum(did, s, err)
		}
	}
}

func (p *plugin) vacuumIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	var ids []string
	for _, inst := range p.contexts {
		if d := inst.settings.DID; inst.action == actVacuum && d != "" && !seen[d] {
			seen[d] = true
			ids = append(ids, d)
		}
	}
	// The info bar's vacuum is read too, once it has been found.
	if p.barDID != "" && !seen[p.barDID] && p.hasBar() {
		ids = append(ids, p.barDID)
	}
	return ids
}

// hasBar reports whether an info bar is on the deck. The caller holds p.mu.
func (p *plugin) hasBar() bool {
	for _, inst := range p.contexts {
		if inst.action == actVacuumBar {
			return true
		}
	}
	return false
}

// findBarVacuum picks the vacuum the info bar shows: the account's one vacuum,
// preferring one that is online if there are several. Nothing is asked of the
// cloud unless a bar is on the deck and the vacuum is not yet known.
func (p *plugin) findBarVacuum() {
	p.mu.Lock()
	need := p.hasBar() && p.barDID == "" && !time.Now().Before(p.barRetryAt)
	p.mu.Unlock()
	if !need || !p.dreame.HasLogin() {
		return
	}
	vs, err := p.dreame.Vacuums()
	var did string
	if err == nil {
		for _, v := range vs {
			if did == "" || v.Online {
				did = v.DID
			}
			if v.Online {
				break
			}
		}
		if did == "" {
			err = errNoVacuum
		}
	}
	p.mu.Lock()
	changed := err != nil && (p.barErr == nil || p.barErr.Error() != err.Error())
	p.barDID, p.barErr = did, err
	// Do not sign in again at once after a failure: a refused login is held off
	// as long as the keys' are, to stay clear of an account lockout.
	p.barRetryAt = time.Time{}
	if err != nil {
		p.barRetryAt = time.Now().Add(30 * time.Second)
		if strings.Contains(err.Error(), "login rejected") {
			p.barRetryAt = time.Now().Add(vacLoginRetry)
		}
	}
	p.mu.Unlock()
	if err != nil {
		if changed {
			log.Printf("dreame info bar: %v", err)
		}
		p.renderBars()
	}
}

var errNoVacuum = errors.New("no vacuum on this Dreame account")

// renderBars repaints every info bar on the deck.
func (p *plugin) renderBars() {
	p.mu.Lock()
	var bars []*instance
	for _, inst := range p.contexts {
		if inst.action == actVacuumBar {
			bars = append(bars, inst)
		}
	}
	p.mu.Unlock()
	for _, inst := range bars {
		p.renderBar(inst)
	}
}

// barModel is what the info bar shows right now.
func (p *plugin) barModel() render.Infobar {
	p.mu.Lock()
	did, ferr := p.barDID, p.barErr
	v := p.vacs[did]
	p.mu.Unlock()

	bar := render.Infobar{Battery: -1, Minutes: -1, Area: -1}
	switch {
	case !p.dreame.HasLogin():
		bar.Note = "NO LOGIN"
	case ferr != nil:
		bar.Note = p.vacuumProblem(ferr)
		if errors.Is(ferr, errNoVacuum) {
			bar.Note = "NO VACUUM"
		}
	case did == "" || (!v.have && v.err == nil):
		bar.Note = "CONNECTING"
	case v.err != nil:
		bar.Note = p.vacuumProblem(v.err)
	default:
		bar.Label, bar.Active = v.state.Label, v.state.Active
		set := func(dst *int, src *int) {
			if src != nil {
				*dst = *src
			}
		}
		set(&bar.Battery, v.state.Battery)
		set(&bar.Minutes, v.state.Minutes)
		set(&bar.Area, v.state.Area)
		if v.state.Error != nil {
			bar.Error = *v.state.Error
		}
	}
	return bar
}

// renderBar paints the Stream Deck Neo's info bar from the account's vacuum.
func (p *plugin) renderBar(inst *instance) {
	img, err := render.InfobarImage(p.barModel())
	if err != nil {
		log.Printf("info bar render: %v", err)
		return
	}
	// The bar is a layout with one picture in it, like the dials' touch strips.
	p.sd.SetFeedback(inst.context, map[string]any{"strip": img})
}

// pressVacuum runs what the key is set to do: start or pause, or go home.
func (p *plugin) pressVacuum(context string, cfg settings) {
	if cfg.DID == "" {
		p.sd.ShowAlert(context)
		return
	}
	var s dreame.State
	var err error
	what := dreame.Toggle
	switch cfg.Mode {
	case "dock":
		what = dreame.Dock
		s, err = p.dreame.Do(cfg.DID, what)
	case "room":
		if cfg.Room < 1 {
			// A room key with no room is a setup problem, not a vacuum one.
			p.sd.ShowAlert(context)
			return
		}
		what = "room " + strconv.Itoa(cfg.Room)
		s, err = p.dreame.DoRoom(cfg.DID, cfg.Room, cfg.Repeat)
	default:
		s, err = p.dreame.Do(cfg.DID, what)
	}
	if err != nil {
		log.Printf("dreame %s: %v", what, err)
		p.sd.ShowAlert(context)
	}
	p.setVacuum(cfg.DID, s, err)
}

// setVacuum records a vacuum's latest state (or failure) and repaints its keys.
func (p *plugin) setVacuum(did string, s dreame.State, err error) {
	p.mu.Lock()
	prev := p.vacs[did]
	// Log a failure when it starts or changes, not on every poll.
	if err != nil && (prev.err == nil || prev.err.Error() != err.Error()) {
		log.Printf("dreame %s: %v", did, err)
	}
	if err != nil {
		prev.err = err
		prev.retryAt = time.Time{}
		if strings.Contains(err.Error(), "login rejected") {
			prev.retryAt = time.Now().Add(vacLoginRetry)
		}
		p.vacs[did] = prev
	} else {
		p.vacs[did] = vacView{state: s, have: true}
	}
	var keys []*instance
	for _, inst := range p.contexts {
		if inst.action == actVacuum && inst.settings.DID == did {
			keys = append(keys, inst)
		}
	}
	p.mu.Unlock()
	for _, inst := range keys {
		p.renderVacuum(inst)
	}
	// The info bar shows the same vacuum, once it is the one it found.
	p.mu.Lock()
	isBar := did == p.barDID
	p.mu.Unlock()
	if isBar {
		p.renderBars()
	}
}

// renderVacuum paints one vacuum key from what the vacuum last reported.
func (p *plugin) renderVacuum(inst *instance) {
	p.mu.Lock()
	did, name := inst.settings.DID, strings.TrimSpace(inst.settings.Name)
	v := p.vacs[did]
	rooms := p.rooms
	p.mu.Unlock()

	if name == "" {
		name = "Vacuum"
	}
	key := render.Vacuum{Name: name, Battery: -1, Press: pressLabel(inst.settings, rooms)}
	switch {
	case did == "":
		key.Note = "SET VACUUM"
	case v.err != nil:
		key.Note = p.vacuumProblem(v.err)
	case !v.have:
		key.Note = "CONNECTING"
	default:
		key.Label, key.Active = v.state.Label, v.state.Active
		if v.state.Battery != nil {
			key.Battery = *v.state.Battery
		}
	}
	img, err := render.VacuumKey(key)
	if err != nil {
		log.Printf("vacuum render: %v", err)
		return
	}
	p.sd.SetImage(inst.context, img)
}

// vacuumProblem is the few words a key has room for when the vacuum cannot be
// reached. The full error is in the log.
func (p *plugin) vacuumProblem(err error) string {
	switch {
	case errors.Is(err, dreame.ErrNotSetUp):
		return "NOT SET UP"
	case strings.Contains(err.Error(), "login rejected"):
		return "BAD LOGIN"
	case !p.dreame.HasLogin():
		return "NO LOGIN"
	}
	return "OFFLINE"
}

// sendVacuums lists the account's vacuums for an open inspector.
func (p *plugin) sendVacuums(ev sd.Event) {
	vs, err := p.dreame.Vacuums()
	if err != nil {
		log.Printf("dreame list: %v", err)
		p.sd.SendToPropertyInspector(ev.Context, ev.Action, map[string]any{"vacuumsError": err.Error()})
		return
	}
	if vs == nil {
		vs = []dreame.Vacuum{}
	}
	p.sd.SendToPropertyInspector(ev.Context, ev.Action, map[string]any{"vacuums": vs})
}

// pressLabel is what a press on this key does, in a few characters. A room
// key says the room's name when it has been given one, and its number when not.
func pressLabel(cfg settings, rooms map[int]string) string {
	switch cfg.Mode {
	case "dock":
		return "DOCK"
	case "room":
		if cfg.Room < 1 {
			return "ROOM ?"
		}
		if name := rooms[cfg.Room]; name != "" {
			return strings.ToUpper(name)
		}
		return "ROOM " + strconv.Itoa(cfg.Room)
	}
	return "START/PAUSE"
}

// parseRooms reads the room names the inspector saves: one "number = name" per
// line, with ":" also accepted. Lines that are not that are skipped, so a
// half-typed list does not lose the lines above it.
func parseRooms(text string) map[int]string {
	rooms := map[int]string{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.IndexAny(line, "=:")
		if i < 0 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(line[:i]))
		name := strings.TrimSpace(line[i+1:])
		if err != nil || n < 1 || n > 99 || name == "" {
			continue
		}
		rooms[n] = name
	}
	return rooms
}
