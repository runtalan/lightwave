package config

import "sync"

// resetConfigDirForTest re-arms the sync.Once behind ConfigDir so a test that
// has just repointed HOME gets a fresh temp directory instead of the real
// one.
//
// This exists because SaveSettings and SaveSlotFile write to the live config
// path with no seam: a test that calls either without redirecting HOME first
// overwrites the user's actual settings and pad map. Call this immediately
// after t.Setenv("HOME", t.TempDir()) in any test that touches persistence.
func resetConfigDirForTest() {
	configDirOnce = sync.Once{}
	configDirPath = ""
	configDirErr = nil
}
