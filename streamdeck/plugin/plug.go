package main

import (
	"encoding/json"
	"errors"
	"log"
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

// A plug that refused the login is left alone this long before trying again.
// Retrying a wrong password every few seconds does nothing but get the plug to
// start throttling the connections (TPAP answers STAT_ACCESS_ERROR), so it is
// asked again at this pace, or at once when the login or the key changes.
const loginRetry = time.Minute

// A plug that keeps failing to answer is asked less and less often, up to this
// gap. Each try at one that has gone away costs a discovery broadcast and
// several seconds of waiting in the helper.
const offlineRetryMax = 30 * time.Second

// plugView is the last thing known about one plug.
type plugView struct {
	reading tapo.Reading
	have    bool
	err     error
	// fails is how many reads in a row have failed.
	fails int
	// retryAt, when set, holds off polling until then.
	retryAt time.Time
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
	p.tapo.SetLogin(strings.TrimSpace(g.TapoUser), g.TapoPass)
	p.clearBackoff("")
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

// clearBackoff lets a held-off plug be tried again now: every plug for "", or
// just one. A new login or a re-pointed key is a reason to retry.
func (p *plugin) clearBackoff(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for h, v := range p.plugs {
		if host == "" || h == host {
			v.retryAt, v.fails = time.Time{}, 0
			p.plugs[h] = v
		}
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
func (p *plugin) plugLoop() {
	t := time.NewTicker(plugPoll)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-p.plugWake:
		}
		for _, host := range p.plugsDue() {
			go func(host string) {
				r, err := p.tapo.State(host)
				p.mu.Lock()
				delete(p.plugBusy, host)
				p.mu.Unlock()
				p.setPlug(host, r, err)
			}(host)
		}
	}
}

// plugsDue is every plug on the deck that is to be read now, marked as being
// read. Each plug keeps its own pace: one that is slow to answer, or not
// answering, is skipped until it has, rather than holding up the rest.
func (p *plugin) plugsDue() []string {
	hosts := p.plugHosts()
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	var due []string
	for _, h := range hosts {
		if p.plugBusy[h] || now.Before(p.plugs[h].retryAt) {
			continue
		}
		p.plugBusy[h] = true
		due = append(due, h)
	}
	return due
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
		prev.retryAt = time.Time{}
		switch {
		case strings.Contains(err.Error(), "login rejected"):
			prev.retryAt = time.Now().Add(loginRetry)
		case prev.fails > 1:
			// One miss may be a blip; after that, back off.
			prev.retryAt = time.Now().Add(offlineHold(prev.fails))
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

// offlineHold is how long to leave a plug that has failed fails reads in a row:
// one poll's gap after the second, doubling from there up to offlineRetryMax.
func offlineHold(fails int) time.Duration {
	d := plugPoll
	for i := 2; i < fails && d < offlineRetryMax; i++ {
		d *= 2
	}
	return min(d, offlineRetryMax)
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
