package main

import (
	"os"
	"testing"
)

// TestMain points HOME at a scratch directory before any test runs.
// config.ConfigDir resolves once per process, and setters such as
// SetFrontWarmth persist settings with no seam, so without this whichever
// test reached it first could pin the real config directory and let a later
// test overwrite the user's settings.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lightwave-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
