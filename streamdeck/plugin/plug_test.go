package main

import (
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"lightwave-sd/internal/dreame"
	"lightwave-sd/internal/sd"
	"lightwave-sd/internal/tapo"
)

func TestPlugProblemSaysWhatToFix(t *testing.T) {
	p := &plugin{tapo: tapo.NewClient()}
	cases := []struct {
		err  error
		want string
	}{
		{tapo.ErrNotSetUp, "NOT SET UP"},
		{fmt.Errorf("wrapped: %w", tapo.ErrNotSetUp), "NOT SET UP"},
		{errors.New("login rejected: check the TP-Link account email and password"), "BAD LOGIN"},
		{errors.New("login rejected: check the TP-Link account email and password (3 attempts left before the plug locks)"), "BAD LOGIN"},
		{errors.New("plug locked: too many failed logins, retry in 5 minutes"), "LOCKED"},
		// No account entered yet, and the plug would not answer: say so, rather
		// than leaving "OFFLINE" for a plug that is only waiting on a login.
		{errors.New("timed out waiting for the plug"), "NO LOGIN"},
	}
	for _, c := range cases {
		if got := p.plugProblem(c.err); got != c.want {
			t.Errorf("plugProblem(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	p.tapo.SetLogin("me@example.com", "pw")
	if got := p.plugProblem(errors.New("timed out waiting for the plug")); got != "OFFLINE" {
		t.Errorf("with a login set, an unreachable plug = %q, want OFFLINE", got)
	}
}

func TestPlugHostsAreDistinctAndTapoOnly(t *testing.T) {
	p := &plugin{contexts: map[string]*instance{
		"a": {action: actTapo, settings: settings{Host: "10.0.0.5"}},
		"b": {action: actTapo, settings: settings{Host: "10.0.0.5"}}, // second key, same plug
		"c": {action: actTapo, settings: settings{Host: "10.0.0.6"}},
		"d": {action: actTapo},                                      // not configured yet
		"e": {action: actPad, settings: settings{Host: "10.0.0.9"}}, // not a plug key
	}}
	got := p.plugHosts()
	sort.Strings(got)
	if len(got) != 2 || got[0] != "10.0.0.5" || got[1] != "10.0.0.6" {
		t.Fatalf("plugHosts = %v", got)
	}
}

func TestRejectedLoginIsNotRetriedEveryPoll(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}}
	p.setPlug("h", tapo.Reading{}, errors.New("login rejected: check the TP-Link account email and password"))
	if !time.Now().Before(p.plugs["h"].retryAt) {
		t.Fatal("a rejected login should hold the plug off")
	}
	p.releaseOffline("h")
	if p.plugs["h"].retryAt.IsZero() {
		t.Fatal("the key coming back on screen released a refusal hold")
	}
	p.clearBackoff()
	if !p.plugs["h"].retryAt.IsZero() {
		t.Fatal("a new login should release the hold")
	}
	// A plug that misses one read keeps its normal pace.
	p.plugs["h"] = plugView{}
	p.setPlug("h", tapo.Reading{}, errors.New("timed out waiting for the plug"))
	if !p.plugs["h"].retryAt.IsZero() {
		t.Fatal("one missed read should not back off")
	}
}

func TestUnreachablePlugIsAskedLessOftenUntilItAnswers(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}}
	gone := errors.New("timed out waiting for the plug")
	var last time.Duration
	for i := 0; i < 8; i++ {
		p.setPlug("h", tapo.Reading{}, gone)
		var hold time.Duration
		if at := p.plugs["h"].retryAt; !at.IsZero() {
			hold = time.Until(at)
		}
		if i > 0 && hold < last-time.Second {
			t.Fatalf("failure %d held %v, less than the %v before it", i+1, hold, last)
		}
		if hold > offlineRetry {
			t.Fatalf("failure %d held %v, past the %v cap", i+1, hold, offlineRetry)
		}
		last = hold
	}
	if last < offlineRetry-time.Second {
		t.Fatalf("a plug gone for a while is held %v, want about %v", last, offlineRetry)
	}
	p.setPlug("h", tapo.Reading{On: true}, nil)
	if v := p.plugs["h"]; v.fails != 0 || !v.retryAt.IsZero() {
		t.Fatalf("an answer should reset the backoff, got %+v", v)
	}
}

func TestRefusingPlugIsLeftAlone(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}}
	p.setPlug("h", tapo.Reading{}, errors.New("login rejected: check the TP-Link account email and password"))
	if time.Until(p.plugs["h"].retryAt) < loginRetry-time.Second {
		t.Fatal("a refusing plug should be left for the login retry period")
	}
}

func TestPlugThatKeepsRefusingIsGivenLongerToRecover(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}}
	refused := errors.New("login rejected: check the TP-Link account email and password")
	empty := errors.New("get_device_info not found in {} for device h")
	hold := func() time.Duration { return time.Until(p.plugs["h"].retryAt).Round(time.Second) }

	var got []time.Duration
	for i := 0; i < 5; i++ {
		p.setPlug("h", tapo.Reading{}, refused)
		got = append(got, hold())
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, refusedRetry}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("holds = %v, want %v", got, want)
		}
	}
	// A login that gets through but whose read fails is still in the lockout.
	p.setPlug("h", tapo.Reading{}, empty)
	if hold() != refusedRetry {
		t.Fatalf("a failed read after refusals held %v, want %v", hold(), refusedRetry)
	}
	p.setPlug("h", tapo.Reading{On: true}, nil)
	if v := p.plugs["h"]; v.refusals != 0 || !v.retryAt.IsZero() {
		t.Fatalf("an answer should reset the schedule, got %+v", v)
	}
}

func TestVacuumProblemSaysWhatToFix(t *testing.T) {
	p := &plugin{dreame: dreame.NewClient()}
	if got := p.vacuumProblem(dreame.ErrNotSetUp); got != "NOT SET UP" {
		t.Errorf("got %q", got)
	}
	if got := p.vacuumProblem(errors.New("login rejected: check the Dreamehome email")); got != "BAD LOGIN" {
		t.Errorf("got %q", got)
	}
	if got := p.vacuumProblem(errors.New("the Dreame cloud did not answer")); got != "NO LOGIN" {
		t.Errorf("no account set: got %q", got)
	}
	p.dreame.SetLogin("me@example.com", "pw", "us")
	if got := p.vacuumProblem(errors.New("the Dreame cloud did not answer")); got != "OFFLINE" {
		t.Errorf("got %q", got)
	}
}

func TestRejectedDreameLoginBacksOffUntilTheLoginChanges(t *testing.T) {
	p := &plugin{vacs: map[string]vacView{}, contexts: map[string]*instance{}}
	p.setVacuum("1", dreame.State{}, errors.New("login rejected: check the Dreamehome email, password and region"))
	if !time.Now().Before(p.vacs["1"].retryAt) {
		t.Fatal("a rejected login should hold the vacuum off")
	}
	p.clearVacBackoff("1")
	if !p.vacs["1"].retryAt.IsZero() {
		t.Fatal("a new login or key should release the hold")
	}
	p.setVacuum("1", dreame.State{}, errors.New("the Dreame cloud did not answer"))
	if !p.vacs["1"].retryAt.IsZero() {
		t.Fatal("only login failures should back off")
	}
}

func TestInspectorSavedLoginReachesBothClients(t *testing.T) {
	p := &plugin{
		tapo: tapo.NewClient(), dreame: dreame.NewClient(),
		plugs: map[string]plugView{}, vacs: map[string]vacView{},
		plugWake: make(chan struct{}, 1), vacWake: make(chan struct{}, 1),
	}
	// What an inspector sends along after saving: the plugin must not depend on
	// Stream Deck announcing the change.
	p.sendInspector(sd.Event{Event: "sendToPlugin", Action: actVacuum, Payload: sd.Payload{
		Want:    "globals",
		Globals: []byte(`{"dreameUser":" me@example.com ","dreamePass":"pw","dreameCountry":"EU","tapoUser":"t@example.com","tapoPass":"tp"}`),
	}})
	if !p.dreame.HasLogin() || !p.tapo.HasLogin() {
		t.Fatalf("login not applied: dreame=%v tapo=%v", p.dreame.HasLogin(), p.tapo.HasLogin())
	}
}

func TestPressLabelNamesWhatTheKeyDoes(t *testing.T) {
	rooms := map[int]string{1: "Living Room", 2: "Kitchen"}
	for cfg, want := range map[settings]string{
		{}:                      "START/PAUSE",
		{Mode: "dock"}:          "DOCK",
		{Mode: "room", Room: 1}: "LIVING ROOM",
		{Mode: "room", Room: 2}: "KITCHEN",
		{Mode: "room", Room: 7}: "ROOM 7", // not named yet
		{Mode: "room"}:          "ROOM ?",
	} {
		if got := pressLabel(cfg, rooms); got != want {
			t.Errorf("pressLabel(%+v) = %q, want %q", cfg, got, want)
		}
	}
}

func TestParseRooms(t *testing.T) {
	got := parseRooms("1 = Living Room\n2: Kitchen\nnonsense\n  3=Hallway  \n0=Zero\nx=Bad\n4 =\n100=Big\n")
	want := map[int]string{1: "Living Room", 2: "Kitchen", 3: "Hallway"}
	if len(got) != len(want) {
		t.Fatalf("parseRooms = %v, want %v", got, want)
	}
	for n, name := range want {
		if got[n] != name {
			t.Errorf("room %d = %q, want %q", n, got[n], name)
		}
	}
}

func TestBarModelSaysWhyWhenItCannotShowTheVacuum(t *testing.T) {
	p := &plugin{dreame: dreame.NewClient(), vacs: map[string]vacView{}}
	if got := p.barModel().Note; got != "NO LOGIN" {
		t.Errorf("no account: %q", got)
	}
	p.dreame.SetLogin("me@example.com", "pw", "us")
	if got := p.barModel().Note; got != "CONNECTING" {
		t.Errorf("not found yet: %q", got)
	}
	p.barErr = errNoVacuum
	if got := p.barModel().Note; got != "NO VACUUM" {
		t.Errorf("empty account: %q", got)
	}
	p.barErr = errors.New("login rejected: check the Dreamehome email, password and region")
	if got := p.barModel().Note; got != "BAD LOGIN" {
		t.Errorf("refused: %q", got)
	}
}

func TestBarModelShowsTheVacuumsRun(t *testing.T) {
	n := func(v int) *int { return &v }
	p := &plugin{dreame: dreame.NewClient(), barDID: "1", vacs: map[string]vacView{
		"1": {have: true, state: dreame.State{Label: "CLEANING", Active: true, Battery: n(64), Minutes: n(12), Area: n(8)}},
	}}
	p.dreame.SetLogin("me@example.com", "pw", "us")
	b := p.barModel()
	if b.Note != "" || b.Label != "CLEANING" || !b.Active || b.Battery != 64 || b.Minutes != 12 || b.Area != 8 || b.Error != 0 {
		t.Fatalf("bar = %+v", b)
	}
	// A vacuum that reports nothing for a field shows it as unknown, not zero.
	p.vacs["1"] = vacView{have: true, state: dreame.State{Label: "IDLE"}}
	if b := p.barModel(); b.Battery != -1 || b.Minutes != -1 || b.Area != -1 {
		t.Fatalf("unknown fields should be -1: %+v", b)
	}
}

func TestOnlyTheBarsVacuumIsPolledWhenABarIsOnTheDeck(t *testing.T) {
	p := &plugin{barDID: "7", contexts: map[string]*instance{}}
	if ids := p.vacuumIDs(); len(ids) != 0 {
		t.Fatalf("no bar yet, got %v", ids)
	}
	p.contexts["b"] = &instance{action: actVacuumBar}
	if ids := p.vacuumIDs(); len(ids) != 1 || ids[0] != "7" {
		t.Fatalf("with a bar, got %v", ids)
	}
	// A vacuum key for the same vacuum does not double it.
	p.contexts["k"] = &instance{action: actVacuum, settings: settings{DID: "7"}}
	if ids := p.vacuumIDs(); len(ids) != 1 {
		t.Fatalf("same vacuum twice: %v", ids)
	}
}

func TestAnAnswerFromBeforeALoginChangeIsIgnored(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}, plugWake: make(chan struct{}, 1)}
	p.setPlug("h", tapo.Reading{}, tapo.ErrLoginChanged)
	if v, ok := p.plugs["h"]; ok {
		t.Fatalf("recorded a stale failure: %+v", v)
	}
	select {
	case <-p.plugWake:
	default:
		t.Fatal("should ask again under the new login")
	}
}

func TestAnOfflinePlugIsAskedAgainWhenItsKeyReappears(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}}
	gone := errors.New("timed out waiting for the plug")
	p.setPlug("h", tapo.Reading{}, gone)
	p.setPlug("h", tapo.Reading{}, gone)
	if p.plugs["h"].retryAt.IsZero() {
		t.Fatal("two misses should back off")
	}
	p.releaseOffline("h")
	if !p.plugs["h"].retryAt.IsZero() {
		t.Fatal("an offline plug should be asked again when its key reappears")
	}
}

func TestAPlugLowOnLoginsIsLeftForSomeoneToFixTheLogin(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}}
	rejected := func(left int) error {
		return fmt.Errorf("login rejected: check the TP-Link account email and password (%d attempts left before the plug locks)", left)
	}
	p.setPlug("h", tapo.Reading{}, rejected(reserveAttempts+1))
	if p.plugs["h"].parked {
		t.Fatal("parked with attempts to spare")
	}
	p.setPlug("h", tapo.Reading{}, rejected(reserveAttempts))
	v := p.plugs["h"]
	if !v.parked || !v.held(time.Now().Add(24*time.Hour)) {
		t.Fatalf("a plug down to its reserve should not be polled again: %+v", v)
	}
	p.releaseOffline("h")
	if !p.plugs["h"].parked {
		t.Fatal("the key reappearing spent a reserved login")
	}
	p.clearBackoff()
	if p.plugs["h"].held(time.Now()) {
		t.Fatal("a new login should get one try")
	}
	p.setPlug("h", tapo.Reading{On: true}, nil)
	if v := p.plugs["h"]; v.parked || v.held(time.Now()) {
		t.Fatalf("an answer should clear everything: %+v", v)
	}
}

func TestALockedPlugIsLeftUntilItOpens(t *testing.T) {
	p := &plugin{plugs: map[string]plugView{}, contexts: map[string]*instance{}}
	p.setPlug("h", tapo.Reading{}, errors.New("plug locked: too many failed logins, retry in 27 minutes"))
	v := p.plugs["h"]
	if got := time.Until(v.lockedUntil).Round(time.Minute); got != 28*time.Minute {
		t.Fatalf("held %v, want 28m", got)
	}
	// Not even a new login gets through a lockout.
	p.clearBackoff()
	if !p.plugs["h"].held(time.Now()) {
		t.Fatal("a new login released a locked plug")
	}
	if !p.plugs["h"].held(time.Now().Add(27 * time.Minute)) {
		t.Fatal("released before the lockout ends")
	}
	if p.plugs["h"].held(time.Now().Add(29 * time.Minute)) {
		t.Fatal("still held after the lockout")
	}

	p.setPlug("u", tapo.Reading{}, errors.New("plug locked: too many failed logins"))
	if got := time.Until(p.plugs["u"].lockedUntil).Round(time.Minute); got != refusedRetry {
		t.Fatalf("a lockout of unknown length held %v, want %v", got, refusedRetry)
	}
}
