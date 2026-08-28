package midi

import "testing"

func TestNoteTrigger(t *testing.T) {
	cases := []struct {
		name string
		msg  []byte
		want uint8
		ok   bool
	}{
		{"note on ch1", []byte{0x90, 62, 100}, 62, true},
		{"note on ch10", []byte{0x99, 62, 1}, 62, true},
		{"cc press", []byte{0xB0, 62, 127}, 62, true},
		{"note off", []byte{0x80, 62, 0}, 0, false},
		{"note on vel 0 is a release", []byte{0x90, 62, 0}, 0, false},
		{"cc value 0 is a release", []byte{0xB0, 62, 0}, 0, false},
		{"different note", []byte{0x90, 63, 100}, 0, false},
		{"short message", []byte{0x90, 62}, 0, false},
		{"system message", []byte{0xF8, 62, 100}, 0, false},
	}
	for _, c := range cases {
		got, ok := NoteTrigger(c.msg, 62)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: NoteTrigger(%v, 62) = (%d,%v), want (%d,%v)", c.name, c.msg, got, ok, c.want, c.ok)
		}
	}
	// 0 means unassigned and must never fire, whatever arrives.
	if _, ok := NoteTrigger([]byte{0x90, 0, 100}, 0); ok {
		t.Error("recall note 0 (unassigned) must not trigger")
	}
}

// A press must be reported exactly once: the release that follows every real
// key press must not fire the action a second time.
func TestNoteTriggerPressReleasePair(t *testing.T) {
	fires := 0
	for _, m := range [][]byte{{0x90, 62, 100}, {0x80, 62, 0}} {
		if _, ok := NoteTrigger(m, 62); ok {
			fires++
		}
	}
	if fires != 1 {
		t.Errorf("press+release fired %d times, want 1", fires)
	}
}
