package midi

import "testing"

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
	// Hardcoded: 60 = down (−), 61 = up (+), even if configured plus/minus differ.
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
	// 60/61 win over a swapped plus/minus config (old default was plus=60).
	if d, ok := PaletteDelta(60, 60, 61); !ok || d != -1 {
		t.Fatalf("note 60 vs plus=60 = (%d,%v), want down", d, ok)
	}
}
