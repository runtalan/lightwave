package main

import (
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lightwave-sd/internal/render"
	"lightwave-sd/internal/sd"
	"lightwave-sd/internal/tapo"
)

// How often every Tapo key on the deck is re-read. Power draw is the reason:
// a few seconds is live enough to watch a kettle boil without hammering the
// plugs.
const plugPoll = 3 * time.Second

// A plug that refused the login is left alone this long before trying again,
// doubling each time it refuses again, up to refusedRetry. A Tapo plug counts
// every rejected login and locks itself after 15 (TPAP answers DEVICE_BLOCKED
// until the lockout ends), so retrying soon does nothing but use them up. It is
// asked again at once when the login changes.
const (
	loginRetry   = time.Minute
	refusedRetry = 10 * time.Minute
)

// A plug that says it has this few attempts left before it locks is not asked
// again by itself at all, only when the login changes or its key is pressed.
// The rest are for someone fixing the login, not for polling.
const reserveAttempts = 10

var (
	attemptsLeftRE = regexp.MustCompile(`\((\d+) attempts? left`)
	lockedForRE    = regexp.MustCompile(`retry in (\d+) minute`)
)

// attemptsLeft is how many more rejected logins the plug will take before it
// locks, when it said.
func attemptsLeft(err error) (int, bool) {
	m := attemptsLeftRE.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	n, convErr := strconv.Atoi(m[1])
	return n, convErr == nil
}

// lockedFor is how long a plug that has locked itself says it will stay
// locked, or 0 when it did not say or is not locked.
func lockedFor(err error) time.Duration {
	if !isLocked(err) {
		return 0
	}
	m := lockedForRE.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return 0
	}
	return time.Duration(n) * time.Minute
}

func isLocked(err error) bool { return strings.Contains(err.Error(), "plug locked") }

// A plug that stops answering is asked less often the longer it stays away, up
// to this. Each attempt is a login, and a plug that does answer again is
// counting them.
const offlineRetry = 30 * time.Second

// Plugs are not polled until Stream Deck has sent the TP-Link login, or this
// long after startup if it never does. A plug asked without the login refuses
// it, and is then held off for loginRetry.
const loginWait = 5 * time.Second

// plugView is the last thing known about one plug.
type plugView struct {
	reading tapo.Reading
	have    bool
	err     error
	// retryAt, when set, holds off polling until then.
	retryAt time.Time
	// fails counts failed reads in a row, and refusals how many of them were
	// the plug refusing the login.
	fails, refusals int
	// parked means the plug is down to its last few logins, and is left alone
	// until the login changes or its key is pressed.
	parked bool
	// lockedUntil is when a plug that locked itself said it would let logins
	// in again. Nothing is sent before then: it would only refuse.
	lockedUntil time.Time
}

// held reports whether the poll should skip the plug at now.
func (v plugView) held(now time.Time) bool {
	return v.parked || now.Before(v.retryAt) || now.Before(v.lockedUntil)
}

// isRefusal reports whether a failure is the plug refusing the login, either
// as wrong or because it has had too many.
func isRefusal(err error) bool {
	return strings.Contains(err.Error(), "login rejected") || isLocked(err)
}

// plugRetry is how long to hold a plug off after a failure. The first failure
// keeps the normal pace. Once a plug has refused the login, every failure until
// it answers again is held off on the refusal schedule: a refusing plug that
// lets one login through can still fail the read, and asking again soon after
// would only lock it out again.
func plugRetry(fails, refusals int) time.Duration {
	if refusals > 0 {
		d := loginRetry << (refusals - 1)
		if d > refusedRetry || d <= 0 {
			d = refusedRetry
		}
		return d
	}
	if fails <= 1 {
		return 0
	}
	d := plugPoll << (fails - 1)
	if d > offlineRetry || d <= 0 {
		d = offlineRetry
	}
	return d
}

// globalSettings are the plugin-wide settings an inspector can change.
type globalSettings struct {
	TapoUser string `json:"tapoUser"`
	TapoPass string `json:"tapoPass"`

	DreameUser    string `json:"dreameUser"`
	DreamePass    string `json:"dreamePass"`
	DreameCountry string `json:"dreameCountry"`
	// DreameRooms is the room names, one "number = name" per line.
	DreameRooms string `json:"dreameRooms"`
}

// onGlobalSettings picks up the TP-Link and Dreame logins. Tapo plugs on current firmware
// refuse local control without the account they were set up under.
func (p *plugin) onGlobalSettings(ev sd.Event) {
	p.applyGlobals(ev.Payload.Settings)
}

// applyGlobals takes the plugin-wide settings, from Stream Deck at startup or
// from an inspector that has just changed them.
func (p *plugin) applyGlobals(raw json.RawMessage) {
	var g globalSettings
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &g)
	}
	// Inspectors resend the login whenever they open, so only a changed login
	// releases a plug held off for refusing the old one.
	changed := p.tapo.SetLogin(strings.TrimSpace(g.TapoUser), g.TapoPass)
	p.mu.Lock()
	p.haveGlobals = true
	p.mu.Unlock()
	if changed {
		p.clearBackoff()
	}
	p.wakePlugs()

	p.dreame.SetLogin(strings.TrimSpace(g.DreameUser), g.DreamePass, strings.ToLower(strings.TrimSpace(g.DreameCountry)))
	// A new login may be a new account, with a different vacuum for the bar.
	p.mu.Lock()
	p.barDID, p.barErr, p.barRetryAt = "", nil, time.Time{}
	p.mu.Unlock()
	p.clearVacBackoff("")
	p.wakeVacuums()

	// Room names label the room keys, so repaint them when the names change.
	p.mu.Lock()
	p.rooms = parseRooms(g.DreameRooms)
	var keys []*instance
	for _, inst := range p.contexts {
		if inst.action == actVacuum {
			keys = append(keys, inst)
		}
	}
	p.mu.Unlock()
	for _, inst := range keys {
		p.renderVacuum(inst)
	}
	p.renderBars()
}

// clearBackoff lets every held-off plug be tried again now, because the login
// changed: a new login deserves one try even at a plug that refused the old
// one. A plug that has locked itself is still left until it said, since it
// would refuse any login before then.
func (p *plugin) clearBackoff() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for h, v := range p.plugs {
		v.retryAt, v.refusals, v.parked = time.Time{}, 0, false
		p.plugs[h] = v
	}
}

// releaseOffline lets a plug held off for not answering be asked again now, as
// when its key comes back on screen. A plug refusing the login keeps its hold:
// pages and profiles change often, and every one would be another login.
func (p *plugin) releaseOffline(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.plugs[host]; ok && v.refusals == 0 && !v.parked && !time.Now().Before(v.lockedUntil) {
		v.retryAt = time.Time{}
		p.plugs[host] = v
	}
}

func (p *plugin) wakePlugs() {
	select {
	case p.plugWake <- struct{}{}:
	default:
	}
}

// plugLoop re-reads every plug that has a key on screen. Nothing runs while no
// Tapo key is on the deck, so a deck without one never starts Python.
//
// Each plug is read on its own: a plug that has gone away takes up to the full
// call timeout to fail, and the others keep their pace meanwhile. A plug still
// being read is skipped rather than asked again.
func (p *plugin) plugLoop() {
	t := time.NewTicker(plugPoll)
	defer t.Stop()
	busy := map[string]bool{}
	done := make(chan string)
	started := time.Now()
	for {
		select {
		case <-t.C:
		case <-p.plugWake:
		case host := <-done:
			delete(busy, host)
			continue
		}
		now := time.Now()
		p.mu.Lock()
		ready := p.haveGlobals
		p.mu.Unlock()
		if !ready && now.Sub(started) < loginWait {
			continue
		}
		for _, host := range p.plugHosts() {
			p.mu.Lock()
			held := p.plugs[host].held(now)
			p.mu.Unlock()
			if held || busy[host] {
				continue
			}
			busy[host] = true
			go func(host string) {
				r, err := p.tapo.State(host)
				p.setPlug(host, r, err)
				done <- host
			}(host)
		}
	}
}

// plugHosts is every distinct plug address among the Tapo keys on the deck.
func (p *plugin) plugHosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	var hosts []string
	for _, inst := range p.contexts {
		if h := inst.settings.Host; inst.action == actTapo && h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// togglePlug flips the plug behind a key and repaints every key on that plug.
func (p *plugin) togglePlug(context, host string) {
	if host == "" {
		p.sd.ShowAlert(context)
		return
	}
	p.mu.Lock()
	locked := time.Now().Before(p.plugs[host].lockedUntil)
	p.mu.Unlock()
	if locked {
		// It would refuse, and a login during the lockout may extend it.
		p.sd.ShowAlert(context)
		return
	}
	// A press is a deliberate try, so it goes through even to a plug held off
	// for refusing the login: that is how a fixed login gets picked up.
	r, err := p.tapo.Toggle(host)
	if err != nil {
		log.Printf("tapo toggle %s: %v", host, err)
		p.sd.ShowAlert(context)
	}
	p.setPlug(host, r, err)
}

// setPlug records a plug's latest reading (or failure) and repaints its keys.
func (p *plugin) setPlug(host string, r tapo.Reading, err error) {
	if errors.Is(err, tapo.ErrLoginChanged) {
		// Asked under the old login; the answer under the new one is what counts.
		p.wakePlugs()
		return
	}
	p.mu.Lock()
	prev := p.plugs[host]
	// Log a failure when it starts or changes, not on every poll: the key shows
	// only a word, and this is where the reason is.
	if err != nil && (prev.err == nil || prev.err.Error() != err.Error()) {
		log.Printf("tapo %s: %v", host, err)
	}
	if err != nil {
		// Keep the last good reading around, but the key reports the failure.
		prev.err = err
		prev.fails++
		if isRefusal(err) {
			prev.refusals++
		}
		now := time.Now()
		prev.retryAt = time.Time{}
		if d := plugRetry(prev.fails, prev.refusals); d > 0 {
			prev.retryAt = now.Add(d)
		}
		if isLocked(err) {
			// Ask again a minute after it said it would open; when it did not
			// say, at the longest refusal pace.
			d := refusedRetry
			if m := lockedFor(err); m > 0 {
				d = m + time.Minute
			}
			prev.lockedUntil = now.Add(d)
		}
		if n, ok := attemptsLeft(err); ok && n <= reserveAttempts {
			prev.parked = true
		}
		p.plugs[host] = prev
	} else {
		p.plugs[host] = plugView{reading: r, have: true}
	}
	var keys []*instance
	for _, inst := range p.contexts {
		if inst.action == actTapo && inst.settings.Host == host {
			keys = append(keys, inst)
		}
	}
	p.mu.Unlock()
	for _, inst := range keys {
		p.renderPlug(inst)
	}
}

// renderPlug paints one Tapo key from what its plug last reported.
func (p *plugin) renderPlug(inst *instance) {
	p.mu.Lock()
	host, name := inst.settings.Host, strings.TrimSpace(inst.settings.Name)
	v := p.plugs[host]
	p.mu.Unlock()

	if name == "" {
		name = v.reading.Alias
	}
	if name == "" {
		name = "Tapo Plug"
	}
	key := render.Plug{Name: name}
	switch {
	case host == "":
		key.Note = "SET PLUG"
	case v.err != nil:
		key.Note = p.plugProblem(v.err)
	case !v.have:
		key.Note = "CONNECTING"
	default:
		key.On, key.Watts = v.reading.On, v.reading.Watts
	}
	img, err := render.PlugKey(key)
	if err != nil {
		log.Printf("plug render: %v", err)
		return
	}
	p.sd.SetImage(inst.context, img)
}

// plugProblem is the few words a key has room for when its plug cannot be
// reached. The full error is in the log.
func (p *plugin) plugProblem(err error) string {
	switch {
	case errors.Is(err, tapo.ErrNotSetUp):
		return "NOT SET UP"
	case isLocked(err):
		return "LOCKED"
	case strings.Contains(err.Error(), "login rejected"):
		return "BAD LOGIN"
	case !p.tapo.HasLogin():
		return "NO LOGIN"
	}
	return "OFFLINE"
}

// sendPlugs runs discovery for an open inspector and hands it the result.
func (p *plugin) sendPlugs(ev sd.Event) {
	plugs, err := p.tapo.Discover()
	if err != nil {
		log.Printf("tapo discover: %v", err)
		p.sd.SendToPropertyInspector(ev.Context, ev.Action, map[string]any{"plugsError": err.Error()})
		return
	}
	if plugs == nil {
		plugs = []tapo.Plug{}
	}
	p.sd.SendToPropertyInspector(ev.Context, ev.Action, map[string]any{"plugs": plugs})
}
