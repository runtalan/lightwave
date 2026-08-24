package config

import (
	"os"
	"testing"
)

// SaveSettings rebuilds the file from a Settings value. Anything it does not
// copy is lost — which is how a round trip could silently drop the plug
// account while appearing to succeed.
func TestTapoCredentialsSurviveSaveLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetConfigDirForTest()

	s := DefaultSettings()
	s.TapoEmail = "plugs@example.com"
	s.TapoPassword = "s3cret"
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := LoadSettings()
	if got.TapoEmail != "plugs@example.com" {
		t.Errorf("email = %q, want plugs@example.com", got.TapoEmail)
	}
	if got.TapoPassword != "s3cret" {
		t.Errorf("password did not survive the round trip")
	}

	// A later save that only touches MIDI must not drop the account.
	got.MidiCC = 11
	if err := SaveSettings(got); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	if again := LoadSettings(); again.TapoEmail == "" || again.TapoPassword == "" {
		t.Fatalf("an unrelated save cleared the plug account: %+v", again)
	}
}

// Env must win, so a shared machine can keep the account out of the file.
func TestTapoEnvWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetConfigDirForTest()

	s := DefaultSettings()
	s.TapoEmail = "stored@example.com"
	s.TapoPassword = "stored-pass"
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}

	t.Setenv("TAPO_EMAIL", "env@example.com")
	t.Setenv("TAPO_PASSWORD", "env-pass")
	email, pass := TapoCredentials()
	if email != "env@example.com" || pass != "env-pass" {
		t.Fatalf("env did not win: %q / %q", email, pass)
	}

	os.Unsetenv("TAPO_EMAIL")
	os.Unsetenv("TAPO_PASSWORD")
	email, pass = TapoCredentials()
	if email != "stored@example.com" || pass != "stored-pass" {
		t.Fatalf("stored account not used once env is clear: %q / %q", email, pass)
	}
}

// A half-configured account only yields handshake failures, so both halves
// must be present before anything tries to connect.
func TestTapoPartialCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetConfigDirForTest()

	s := DefaultSettings()
	s.TapoEmail = "only@example.com"
	if err := SaveSettings(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	email, pass := TapoCredentials()
	if email == "" {
		t.Error("email should still be reported")
	}
	if pass != "" {
		t.Errorf("password should be empty, got %q", pass)
	}
}
