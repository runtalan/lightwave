package midi

import (
	"testing"

	"lightwave/internal/config"

	gomidi "gitlab.com/gomidi/midi/v2"
)

// The recall note travels from config through the lock-free packed word and
// back out of TakeNote. This covers that whole path, including the bit-16
// shift that a two-note packing would have silently dropped.
func TestRecallNoteReachesTakeNote(t *testing.T) {
	l := New(config.MIDI{CC: 7, CCAlt: 1, NotePlus: 61, NoteMinus: 60, NoteRecall: 62})

	l.onMIDI(gomidi.Message{0x90, 62, 100}, 0)
	note, _, recall, ok := l.TakeNote()
	if !ok || note != 62 || !recall {
		t.Fatalf("recall press: note=%d recall=%v ok=%v; want 62/true/true", note, recall, ok)
	}
	// Drained exactly once.
	if _, _, _, ok := l.TakeNote(); ok {
		t.Error("second TakeNote returned an event; the first should have drained it")
	}

	// A palette note still routes as a palette step, not a recall.
	l.onMIDI(gomidi.Message{0x90, 61, 100}, 0)
	note, _, recall, ok = l.TakeNote()
	if !ok || note != 61 || recall {
		t.Fatalf("palette press: note=%d recall=%v ok=%v; want 61/false/true", note, recall, ok)
	}

	// Unassigned (0) must never claim a note.
	l2 := New(config.MIDI{NotePlus: 61, NoteMinus: 60, NoteRecall: 0})
	l2.onMIDI(gomidi.Message{0x90, 62, 100}, 0)
	if _, _, recall, ok := l2.TakeNote(); ok && recall {
		t.Error("unassigned recall note fired")
	}

	// Config survives the round trip through the packed word.
	if got := l.cfgLocked().NoteRecall; got != 62 {
		t.Errorf("cfgLocked().NoteRecall = %d, want 62", got)
	}
}
