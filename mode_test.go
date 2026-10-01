package main

import (
	"strings"
	"testing"
)

func TestColorModeFoldsWarmAndGradient(t *testing.T) {
	for _, c := range []struct {
		warm, grad bool
		want       string
	}{
		{true, true, ModeWarmness},
		{true, false, ModeWarmness},
		{false, true, ModePalette},
		{false, false, ModeSolid},
	} {
		a := &App{warmMode: c.warm, gradient: c.grad}
		if got := a.colorModeLocked(); got != c.want {
			t.Fatalf("warm=%v gradient=%v: mode %q, want %q", c.warm, c.grad, got, c.want)
		}
	}
}

// An unknown mode must answer with an error rather than an empty reply, which
// the socket would treat as a window command.
func TestRemoteModeRejectsUnknownMode(t *testing.T) {
	a := &App{}
	if got := a.RemoteCommand("MODE disco"); !strings.HasPrefix(got, "ERR ") {
		t.Fatalf("MODE disco = %q, want ERR", got)
	}
}
