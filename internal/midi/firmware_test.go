package midi

import (
	"testing"

	"lightwave/internal/config"

	gomidi "gitlab.com/gomidi/midi/v2"
)

// The GMMK numpad firmware in use sends:
//
//	Num + turn  -> channel 1, CC 60/61   (palette)
//	Num + click -> channel 2, CC 20      (a button, free for recall)
//	Slider      -> channel 3, CC 62      (brightness)
//
// Channel is deliberately ignored throughout — the same control can move
// between channels across firmware revisions, and matching on the number
// alone is what keeps a remap from silently breaking every binding.
func TestGMMKFirmwareLayout(t *testing.T) {
	l := New(config.MIDI{CC: 62, CCAlt: 7, NotePlus: 60, NoteMinus: 61, NoteRecall: 20, CCMin: 0, CCMax: 117})

	// Num + click fires recall, and its release does not fire again.
	l.onMIDI(gomidi.Message{0xB1, 20, 127}, 0)
	note, _, recall, ok := l.TakeNote()
	if !ok || !recall || note != 20 {
		t.Errorf("Num+click: note=%d recall=%v ok=%v; want 20/true/true", note, recall, ok)
	}
	l.onMIDI(gomidi.Message{0xB1, 20, 0}, 0)
	if _, _, _, ok := l.TakeNote(); ok {
		t.Error("Num+click release fired a second event")
	}

	// Num + turn still steps the palette, not recall.
	l.onMIDI(gomidi.Message{0xB0, 61, 127}, 0)
	note, _, recall, ok = l.TakeNote()
	if !ok || recall || note != 61 {
		t.Errorf("Num+turn: note=%d recall=%v; want 61/false", note, recall)
	}

	// The slider reaches brightness once it has proven it varies, and never
	// surfaces as a note.
	l.onMIDI(gomidi.Message{0xB2, 62, 40}, 0)
	l.onMIDI(gomidi.Message{0xB2, 62, 100}, 0)
	if _, _, _, ok := l.TakeNote(); ok {
		t.Error("slider surfaced as a note event")
	}
	if cc, val, ok := l.TakeCC(); !ok || cc != 62 || val != 100 {
		t.Errorf("slider: cc=%d val=%d ok=%v; want 62/100/true", cc, val, ok)
	}
}

// Assigning recall to the fader's own CC must not swallow the slider: losing
// brightness entirely is far worse than a recall key that does nothing.
func TestRecallOnBrightnessCCDoesNotStealSlider(t *testing.T) {
	l := New(config.MIDI{CC: 62, CCAlt: 7, NotePlus: 60, NoteMinus: 61, NoteRecall: 62})
	l.onMIDI(gomidi.Message{0xB2, 62, 40}, 0)
	l.onMIDI(gomidi.Message{0xB2, 62, 100}, 0)
	if _, _, _, ok := l.TakeNote(); ok {
		t.Error("fader move fired recall; brightness would be lost")
	}
	if cc, _, ok := l.TakeCC(); !ok || cc != 62 {
		t.Errorf("brightness must still work; got cc=%d ok=%v", cc, ok)
	}
}
