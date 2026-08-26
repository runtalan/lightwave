package midi

import (
	"testing"

	"lightwave/internal/config"

	gomidi "gitlab.com/gomidi/midi/v2"
)

func TestCCToPercentRange(t *testing.T) {
	if CCToPercent(0) != 1 {
		t.Fatalf("CC 0 = %d, want 1 (dimmest, not off)", CCToPercent(0))
	}
	if CCToPercent(127) != 100 {
		t.Fatalf("CC 127 = %d, want 100", CCToPercent(127))
	}
	if CCToPercent(126) < 99 {
		t.Fatalf("CC 126 = %d, want at least 99", CCToPercent(126))
	}
	// A full-scale fader must not stall at 92% (117/127*100 truncated).
	if CCToPercent(117) < 92 {
		t.Fatalf("CC 117 = %d, unexpectedly low", CCToPercent(117))
	}
	for v := 0; v <= 127; v++ {
		p := CCToPercent(uint8(v))
		if p < 1 || p > 100 {
			t.Fatalf("CC %d = %d, outside ignited 1–100", v, p)
		}
	}
}

func TestPaletteTriggerNoteAndCC(t *testing.T) {
	// Note On channel 0, vel 100.
	if d, n, cc, ok := PaletteTrigger([]byte{0x90, 60, 100}, 72, 73); !ok || d != -1 || n != 60 || cc {
		t.Fatalf("note on 60 ch0 = delta=%d n=%d cc=%v ok=%v", d, n, cc, ok)
	}
	// Channel 15 must work the same.
	if d, n, cc, ok := PaletteTrigger([]byte{0x9F, 61, 1}, 72, 73); !ok || d != 1 || n != 61 || cc {
		t.Fatalf("note on 61 ch15 = delta=%d n=%d cc=%v ok=%v", d, n, cc, ok)
	}
	// CC 60/61 (GMMK pads often send CC, not notes), value > 0.
	if d, n, cc, ok := PaletteTrigger([]byte{0xB0, 61, 127}, 72, 73); !ok || d != 1 || n != 61 || !cc {
		t.Fatalf("cc 61 = delta=%d n=%d cc=%v ok=%v", d, n, cc, ok)
	}
	if d, n, cc, ok := PaletteTrigger([]byte{0xB3, 60, 64}, 72, 73); !ok || d != -1 || n != 60 || !cc {
		t.Fatalf("cc 60 ch3 = delta=%d n=%d cc=%v ok=%v", d, n, cc, ok)
	}
	// Releases must not step.
	if _, _, _, ok := PaletteTrigger([]byte{0x90, 61, 0}, 72, 73); ok {
		t.Fatal("note on vel 0 must not cycle")
	}
	if _, _, _, ok := PaletteTrigger([]byte{0x80, 61, 64}, 72, 73); ok {
		t.Fatal("note off must not cycle")
	}
	if _, _, _, ok := PaletteTrigger([]byte{0xB0, 61, 0}, 72, 73); ok {
		t.Fatal("cc value 0 must not cycle")
	}
	// Brightness CC 7 is not a palette step.
	if _, _, _, ok := PaletteTrigger([]byte{0xB0, 7, 90}, 72, 73); ok {
		t.Fatal("cc 7 must not cycle the palette")
	}
}

func TestPortScorePrefersGMMK(t *testing.T) {
	if portScore("GMMK Numpad MIDI") <= portScore("Bluetooth MIDI") {
		t.Fatal("GMMK numpad should beat a generic MIDI port")
	}
	if portScore("IAC Driver") >= portScore("USB MIDI") {
		t.Fatal("a port named MIDI should beat one that is not")
	}
	if portScore("Minji Keyboard") != 0 {
		t.Fatal("do not match an unrelated Minji product name")
	}
}

func TestPaletteDeltaNotes60And61(t *testing.T) {
	// 60 = down (−), 61 = up (+) by default, when the config does not claim them.
	if d, ok := PaletteDelta(60, 48, 49); !ok || d != -1 {
		t.Fatalf("note 60 = (%d,%v), want down", d, ok)
	}
	if d, ok := PaletteDelta(61, 48, 49); !ok || d != 1 {
		t.Fatalf("note 61 = (%d,%v), want up", d, ok)
	}
	// Configured plus/minus still work for other keys.
	if d, ok := PaletteDelta(72, 72, 73); !ok || d != 1 {
		t.Fatalf("configured plus = (%d,%v), want up", d, ok)
	}
	if d, ok := PaletteDelta(73, 72, 73); !ok || d != -1 {
		t.Fatalf("configured minus = (%d,%v), want down", d, ok)
	}
	if _, ok := PaletteDelta(50, 72, 73); ok {
		t.Fatal("unrelated note must not step the palette")
	}
	// A config that inverts the stock pair wins: this is the GMMK knob whose
	// firmware sends 60 the direction the user reads as "up". Before, the
	// hardcoded switch silently overrode this and the knob ran backwards.
	if d, ok := PaletteDelta(60, 60, 61); !ok || d != 1 {
		t.Fatalf("note 60 with plus=60 = (%d,%v), want up", d, ok)
	}
	if d, ok := PaletteDelta(61, 60, 61); !ok || d != -1 {
		t.Fatalf("note 61 with minus=61 = (%d,%v), want down", d, ok)
	}
}

func TestTraceOffByDefault(t *testing.T) {
	l := New(config.MIDI{CC: 62, NotePlus: 61, NoteMinus: 60})
	l.onMIDI(gomidi.Message{0xB0, 61, 127}, 0)
	if ev := l.DrainTrace(); len(ev) != 0 {
		t.Fatalf("trace off recorded %d events, want 0", len(ev))
	}
}

func TestTraceRecordsRawMessages(t *testing.T) {
	l := New(config.MIDI{CC: 62, NotePlus: 61, NoteMinus: 60})
	l.SetTrace(true)
	// A relative encoder's two directions, as one CC number with differing
	// values — the shape that no plus/minus remap can distinguish.
	l.onMIDI(gomidi.Message{0xB0, 60, 1}, 0)
	l.onMIDI(gomidi.Message{0xB0, 60, 127}, 0)
	ev := l.DrainTrace()
	if len(ev) != 2 {
		t.Fatalf("got %d events, want 2", len(ev))
	}
	if ev[0].Num != 60 || ev[0].Val != 1 || ev[1].Val != 127 {
		t.Fatalf("events = %+v, want num 60 vals 1 then 127", ev)
	}
	if ev[0].Kind() != "CC" || ev[0].Channel() != 1 {
		t.Fatalf("kind/chan = %s/%d, want CC/1", ev[0].Kind(), ev[0].Channel())
	}
	// Draining twice must not repeat events.
	if again := l.DrainTrace(); len(again) != 0 {
		t.Fatalf("second drain returned %d events, want 0", len(again))
	}
}

func TestTraceRingOverflowKeepsNewest(t *testing.T) {
	l := New(config.MIDI{CC: 62, NotePlus: 61, NoteMinus: 60})
	l.SetTrace(true)
	for i := 0; i < traceRingSize+50; i++ {
		l.onMIDI(gomidi.Message{0xB0, 62, uint8(i % 128)}, 0)
	}
	ev := l.DrainTrace()
	if len(ev) != traceRingSize {
		t.Fatalf("got %d events, want %d (ring cap)", len(ev), traceRingSize)
	}
	// The newest write must survive the wrap.
	last := uint8((traceRingSize + 49) % 128)
	if ev[len(ev)-1].Val != last {
		t.Fatalf("newest val = %d, want %d", ev[len(ev)-1].Val, last)
	}
}
