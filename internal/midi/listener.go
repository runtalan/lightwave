package midi

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"lightwave/internal/config"

	gomidi "gitlab.com/gomidi/midi/v2"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

// packed: bit16 = present, bits 8-15 = cc or note, bits 0-7 = value
const midiPresent uint32 = 1 << 16

// Listener talks to one CoreMIDI/RtMidi input. The CGO callback must only
// store atomics — no mutex, log, Wails, UDP, or channel send. A Go loop
// owned by the app polls TakeCC/TakeNote/TakeStatus.
type Listener struct {
	mu    sync.Mutex
	stop  func()
	port  string
	alive bool

	// learnSeen[cc] holds the first value seen for that CC (0x100 bit = set),
	// learnVary[cc] flips to 1 once a second, different value arrives. Both are
	// fixed-size atomic arrays so the CGO callback stays lock-free, as the
	// contract above requires.
	learnSeen [128]atomic.Uint32
	learnVary [128]atomic.Uint32

	ccWanted   atomic.Uint32 // cc | (alt << 8)
	noteWanted atomic.Uint32 // plus | (minus << 8)
	latestCC   atomic.Uint32
	latestNote atomic.Uint32
	statusOK   atomic.Uint32 // 0/1
	statusSeq  atomic.Uint32
	seenSeq    atomic.Uint32
}

func New(cfg config.MIDI) *Listener {
	l := &Listener{}
	l.storeWanted(cfg)
	return l
}

// noteVarying records a CC value and reports whether this CC has now shown
// more than one distinct value. Lock-free: safe from the CGO callback.
func (l *Listener) noteVarying(cc, val uint8) bool {
	if cc > 127 {
		return false
	}
	if l.learnVary[cc].Load() == 1 {
		return true
	}
	const seenBit = uint32(1) << 8
	prev := l.learnSeen[cc].Load()
	if prev == 0 {
		l.learnSeen[cc].Store(seenBit | uint32(val))
		return false
	}
	if uint8(prev) != val {
		l.learnVary[cc].Store(1)
		return true
	}
	return false
}

// VaryingCCs returns the CC numbers that have sent more than one distinct
// value — i.e. the controls that behave like sliders or knobs.
func (l *Listener) VaryingCCs() []uint8 {
	out := []uint8{}
	for cc := 0; cc < 128; cc++ {
		if l.learnVary[cc].Load() == 1 {
			out = append(out, uint8(cc))
		}
	}
	return out
}

func (l *Listener) storeWanted(cfg config.MIDI) {
	l.ccWanted.Store(uint32(cfg.CC) | uint32(cfg.CCAlt)<<8)
	l.noteWanted.Store(uint32(cfg.NotePlus) | uint32(cfg.NoteMinus)<<8)
}

func (l *Listener) Start() error {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("midi: start recovered (%v); continuing without MIDI", r)
			l.setStatus(false, "")
		}
	}()

	ins := gomidi.GetInPorts()
	if len(ins) == 0 {
		log.Println("midi: no input ports (app continues without hardware)")
		l.setStatus(false, "")
		return nil
	}

	in := ins[0]
	best := portScore(in.String())
	for _, p := range ins[1:] {
		if s := portScore(p.String()); s > best {
			in, best = p, s
		}
	}
	name := in.String()

	// Open only this port. Do not ListenTo every enumerated device.
	stop, err := gomidi.ListenTo(in, l.onMIDI)
	if err != nil {
		log.Printf("midi: listen failed (%v); continuing without MIDI", err)
		l.setStatus(false, "")
		return nil
	}

	l.mu.Lock()
	l.stop = stop
	l.port = name
	l.alive = true
	cfg := l.cfgLocked()
	l.mu.Unlock()
	log.Printf("midi: listening on %s (CC %d/%d, palette notes 60−/61+ and +%d -%d, all channels)", name, cfg.CC, cfg.CCAlt, cfg.NotePlus, cfg.NoteMinus)
	l.setStatus(true, name)
	return nil
}

// packed note: bit16 present, bit17 viaCC, bits 8-15 key/cc number
const midiViaCC uint32 = 1 << 17

// onMIDI runs on the CoreMIDI / RtMidi CGO thread.
// Store numbers only. Never lock, log, emit, or send on a channel.
func (l *Listener) onMIDI(msg gomidi.Message, _ int32) {
	defer func() { _ = recover() }()

	want := l.noteWanted.Load()
	plus, minus := uint8(want), uint8(want>>8)
	if delta, num, viaCC, ok := PaletteTrigger([]byte(msg), plus, minus); ok && delta != 0 {
		packed := midiPresent | uint32(num)<<8
		if viaCC {
			packed |= midiViaCC
		}
		l.latestNote.Store(packed)
		return
	}

	var ch, cc, val uint8
	if msg.GetControlChange(&ch, &cc, &val) {
		if cc == NotePaletteDown || cc == NotePaletteUp {
			return
		}
		wantCC := l.ccWanted.Load()
		if cc == uint8(wantCC) || cc == uint8(wantCC>>8) {
			l.latestCC.Store(midiPresent | uint32(cc)<<8 | uint32(val))
			l.noteVarying(cc, val)
			return
		}
		// Adopt an unconfigured CC only once it proves it is a real
		// continuous control by sending a second, different value. A pad or
		// button that always emits the same number never qualifies.
		if l.noteVarying(cc, val) {
			l.latestCC.Store(midiPresent | uint32(cc)<<8 | uint32(val))
		}
		return
	}
}

func (l *Listener) setStatus(ok bool, port string) {
	l.mu.Lock()
	l.alive = ok
	l.port = port
	l.mu.Unlock()
	if ok {
		l.statusOK.Store(1)
	} else {
		l.statusOK.Store(0)
	}
	l.statusSeq.Add(1)
}

func (l *Listener) TakeCC() (cc, val uint8, ok bool) {
	v := l.latestCC.Swap(0)
	if v&midiPresent == 0 {
		return 0, 0, false
	}
	cc, val = uint8(v>>8), uint8(v)
	if cc == NotePaletteDown || cc == NotePaletteUp {
		return 0, 0, false
	}
	// A control that has only ever reported one value is not a working slider:
	// the GMMK numpad's fader, bound as a button in VIA/QMK, streams a constant
	// 0 many times a second. Acting on it would peg brightness at that value
	// and fight every other brightness source. Ignore it until it proves it
	// varies.
	if cc < 128 && l.learnVary[cc].Load() != 1 {
		return 0, 0, false
	}
	return cc, val, true
}

func (l *Listener) TakeNote() (note uint8, viaCC bool, ok bool) {
	v := l.latestNote.Swap(0)
	if v&midiPresent == 0 {
		return 0, false, false
	}
	return uint8(v >> 8), v&midiViaCC != 0, true
}

func (l *Listener) TakeStatus() (connected bool, port string, changed bool) {
	seq := l.statusSeq.Load()
	if seq == l.seenSeq.Load() {
		return false, "", false
	}
	l.seenSeq.Store(seq)
	l.mu.Lock()
	connected, port = l.alive, l.port
	l.mu.Unlock()
	return connected, port, true
}

func (l *Listener) cfgLocked() config.MIDI {
	w := l.ccWanted.Load()
	n := l.noteWanted.Load()
	return config.MIDI{
		CC:        uint8(w),
		CCAlt:     uint8(w >> 8),
		NotePlus:  uint8(n),
		NoteMinus: uint8(n >> 8),
	}
}

func (l *Listener) Update(cfg config.MIDI) {
	l.storeWanted(cfg)
}

func (l *Listener) Connected() (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.alive, l.port
}

func (l *Listener) Close() {
	l.mu.Lock()
	stop := l.stop
	l.stop = nil
	l.alive = false
	l.mu.Unlock()
	if stop != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("midi: close recovered: %v", r)
				}
			}()
			stop()
		}()
	}
	func() {
		defer func() { _ = recover() }()
		gomidi.CloseDriver()
	}()
}

func CCToPercent(value uint8) int {
	if value >= 127 {
		return 100
	}
	if value == 0 {
		// Fader bottom is dimmest, never lamp-off. Govee brightness 0 is a
		// power-off on both LAN and BLE (H6001 0x33 0x04 0x00).
		return 1
	}
	// Integer map 1–126 → 1–99. (v*100)/127 truncates; 117 would become 92
	// if someone used /128. Rounding keeps a full-throw fader at 100.
	n := (int(value)*100 + 63) / 127
	if n < 1 {
		return 1
	}
	return n
}

// Notes 60 (−) and 61 (+) always cycle the palette, matching the HUD keys.
// GMMK/Glorious pads often emit these as CC rather than Note On; both fire.
const (
	NotePaletteDown uint8 = 60
	NotePaletteUp   uint8 = 61
)

// PaletteDelta maps a note/CC number to a palette step. 60/61 are hardcoded;
// the configured plus/minus notes still fire for any other key.
func PaletteDelta(note, plus, minus uint8) (int, bool) {
	switch note {
	case NotePaletteDown:
		return -1, true
	case NotePaletteUp:
		return 1, true
	}
	if note == plus {
		return 1, true
	}
	if note == minus {
		return -1, true
	}
	return 0, false
}

// PaletteTrigger reads a channel message on any MIDI channel.
// Note On 60/61 (velocity > 0) and CC 60/61 (value > 0) step the palette.
// Note Off, Note On velocity 0, and CC value 0 are releases and must not step.
func PaletteTrigger(msg []byte, plus, minus uint8) (delta int, num uint8, viaCC bool, ok bool) {
	if len(msg) < 3 {
		return 0, 0, false, false
	}
	st := msg[0]
	if st < 0x80 || st >= 0xF0 {
		return 0, 0, false, false
	}
	typ := st >> 4
	n, v := msg[1]&0x7f, msg[2]&0x7f
	switch typ {
	case 0x9: // Note On, any channel
		if v == 0 {
			return 0, 0, false, false
		}
		d, hit := PaletteDelta(n, plus, minus)
		return d, n, false, hit
	case 0x8: // Note Off
		return 0, 0, false, false
	case 0xB: // Control Change
		if v == 0 {
			return 0, 0, false, false
		}
		if n != NotePaletteDown && n != NotePaletteUp {
			return 0, 0, false, false
		}
		d, hit := PaletteDelta(n, plus, minus)
		return d, n, true, hit
	}
	return 0, 0, false, false
}

// portScore prefers a GMMK/Glorious numpad, then any port whose name looks
// like MIDI (including a typo "middy"). Higher wins. Not a product search.
func portScore(name string) int {
	ls := strings.ToLower(name)
	n := 0
	if strings.Contains(ls, "gmmk") || strings.Contains(ls, "glorious") {
		n += 4
	}
	if strings.Contains(ls, "numpad") {
		n += 3
	}
	if strings.Contains(ls, "midi") || strings.Contains(ls, "middy") {
		n += 2
	}
	return n
}

func DescribePorts() string {
	defer func() { _ = recover() }()
	ins := gomidi.GetInPorts()
	if len(ins) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ins))
	for i, p := range ins {
		parts = append(parts, fmt.Sprintf("%d:%s", i, p.String()))
	}
	return strings.Join(parts, ", ")
}
