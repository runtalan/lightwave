package main

import (
	"errors"
	"fmt"
	"sort"
	"testing"

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
