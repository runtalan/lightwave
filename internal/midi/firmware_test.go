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

	// Direction, not just delivery. This firmware is configured plus=60,
	// minus=61 — inverting the stock pair — and the configured values must
	// win, or the knob steps the palette the opposite way from the turn.
	if d, ok := PaletteDelta(60, 60, 61); !ok || d != 1 {
		t.Errorf("Num+turn 60 = (%d,%v), want up", d, ok)
	}
	if d, ok := PaletteDelta(61, 60, 61); !ok || d != -1 {
		t.Errorf("Num+turn 61 = (%d,%v), want down", d, ok)
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

// Channel pinning. "Any" (0) is the default and must accept every channel;
// a pinned channel must accept only that one.
func TestChannelFiltering(t *testing.T) {
	// Pinned to the user's firmware: palette ch 1, recall ch 2, fader ch 3.
	l := New(config.MIDI{
		CC: 62, CCAlt: 7, NotePlus: 60, NoteMinus: 61, NoteRecall: 20,
		ChanCC: 3, ChanPalette: 1, ChanRecall: 2,
	})

	// Recall on its own channel (2 -> status 0xB1) fires.
	l.onMIDI(gomidi.Message{0xB1, 20, 127}, 0)
	if _, _, recall, ok := l.TakeNote(); !ok || !recall {
		t.Error("recall on channel 2 should fire")
	}
	// The same control on another channel does not.
	l.onMIDI(gomidi.Message{0xB4, 20, 127}, 0)
	if _, _, _, ok := l.TakeNote(); ok {
		t.Error("recall on channel 5 fired despite being pinned to 2")
	}

	// Palette on channel 1 (0xB0) fires; channel 8 does not.
	l.onMIDI(gomidi.Message{0xB0, 61, 127}, 0)
	if _, _, _, ok := l.TakeNote(); !ok {
		t.Error("palette on channel 1 should fire")
	}
	l.onMIDI(gomidi.Message{0xB7, 61, 127}, 0)
	if _, _, _, ok := l.TakeNote(); ok {
		t.Error("palette on channel 8 fired despite being pinned to 1")
	}

	// Fader on channel 3 (0xB2) reaches brightness; channel 6 does not.
	l.onMIDI(gomidi.Message{0xB2, 62, 40}, 0)
	l.onMIDI(gomidi.Message{0xB2, 62, 100}, 0)
	if cc, _, ok := l.TakeCC(); !ok || cc != 62 {
		t.Errorf("fader on channel 3 should reach brightness; got cc=%d ok=%v", cc, ok)
	}
	l.onMIDI(gomidi.Message{0xB5, 62, 55}, 0)
	if _, _, ok := l.TakeCC(); ok {
		t.Error("fader on channel 6 reached brightness despite being pinned to 3")
	}
}

// The shipped default is "any", so every control works on every channel —
// this is what keeps a firmware remap from silently breaking a binding.
func TestChannelAnyIsDefault(t *testing.T) {
	if c := (config.Settings{}).MIDI(); c.ChanCC != 0 || c.ChanPalette != 0 || c.ChanRecall != 0 {
		t.Fatalf("default channels = %d/%d/%d, want 0/0/0 (any)", c.ChanCC, c.ChanPalette, c.ChanRecall)
	}
	l := New(config.MIDI{CC: 62, NotePlus: 60, NoteMinus: 61, NoteRecall: 20})
	for _, st := range []byte{0xB0, 0xB5, 0xBF} { // channels 1, 6, 16
		l.onMIDI(gomidi.Message{st, 20, 127}, 0)
		if _, _, recall, ok := l.TakeNote(); !ok || !recall {
			t.Errorf("status 0x%02X: recall should fire on any channel", st)
		}
	}
}

// An out-of-range channel widens to "any" rather than narrowing to a wrong
// channel — a nonsense value must never silently disable a control.
func TestChannelOutOfRangeBecomesAny(t *testing.T) {
	for _, n := range []int{-3, 0, 17, 999} {
		s := config.Settings{MidiChanRecall: n}
		if got := s.MIDI().ChanRecall; got != 0 {
			t.Errorf("channel %d normalised to %d, want 0 (any)", n, got)
		}
	}
	for n := 1; n <= 16; n++ {
		s := config.Settings{MidiChanRecall: n}
		if got := s.MIDI().ChanRecall; int(got) != n {
			t.Errorf("channel %d normalised to %d", n, got)
		}
	}
}

// Palette bound to the fader's own CC must not swallow brightness — same
// reasoning as TestRecallOnBrightnessCCDoesNotStealSlider.
func TestPaletteOnBrightnessCCDoesNotStealSlider(t *testing.T) {
	l := New(config.MIDI{CC: 62, CCAlt: 7, NotePlus: 62, NoteMinus: 61})
	l.onMIDI(gomidi.Message{0xB2, 62, 40}, 0)
	l.onMIDI(gomidi.Message{0xB2, 62, 100}, 0)
	if cc, _, ok := l.TakeCC(); !ok || cc != 62 {
		t.Errorf("brightness must still work; got cc=%d ok=%v", cc, ok)
	}
}
