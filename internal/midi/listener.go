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

	ccWanted    atomic.Uint32 // cc | (alt << 8)
	noteWanted  atomic.Uint32 // plus | (minus << 8) | (recall << 16)
	latestCC    atomic.Uint32
	latestRawCC atomic.Uint32
	latestNote  atomic.Uint32
	statusOK    atomic.Uint32 // 0/1
	statusSeq   atomic.Uint32
	seenSeq     atomic.Uint32
}

func New(cfg config.MIDI) *Listener {
	l := &Listener{}
	l.storeWanted(cfg)
	SetCCRange(cfg.CCMin, cfg.CCMax)
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
	l.noteWanted.Store(uint32(cfg.NotePlus) | uint32(cfg.NoteMinus)<<8 | uint32(cfg.NoteRecall)<<16)
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
	recall := "unassigned"
	if cfg.NoteRecall != 0 {
		recall = fmt.Sprint(cfg.NoteRecall)
	}
	log.Printf("midi: listening on %s (CC %d/%d, palette notes 60−/61+ and +%d -%d, recall %s, all channels)",
		name, cfg.CC, cfg.CCAlt, cfg.NotePlus, cfg.NoteMinus, recall)
	l.setStatus(true, name)
	return nil
}

// packed note: bit16 present, bit17 viaCC, bit18 recall, bits 8-15 key/cc number
const midiViaCC uint32 = 1 << 17
const midiRecall uint32 = 1 << 18

// onMIDI runs on the CoreMIDI / RtMidi CGO thread.
// Store numbers only. Never lock, log, emit, or send on a channel.
func (l *Listener) onMIDI(msg gomidi.Message, _ int32) {
	defer func() { _ = recover() }()

	want := l.noteWanted.Load()
	plus, minus, recall := uint8(want), uint8(want>>8), uint8(want>>16)
	// Recall is checked before the palette match: an explicit assignment
	// should win over the hardcoded 60/61 palette keys.
	//
	// It is deliberately not checked ahead of the brightness CCs. A fader
	// bound to the same number would otherwise fire recall on every step of a
	// sweep and never reach the brightness path, which loses the slider
	// entirely — a far worse failure than a recall key that does nothing. The
	// brightness CCs are only ever CC messages, so notes are unaffected.
	if recall != 0 && !l.isBrightnessCC([]byte(msg), recall) {
		if num, ok := NoteTrigger([]byte(msg), recall); ok {
			l.latestNote.Store(midiPresent | uint32(num)<<8 | midiRecall)
			return
		}
	}
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
			l.latestRawCC.Store(midiPresent | uint32(cc)<<8 | uint32(val))
			l.noteVarying(cc, val)
			return
		}
		// Adopt an unconfigured CC only once it proves it is a real
		// continuous control by sending a second, different value. A pad or
		// button that always emits the same number never qualifies.
		if l.noteVarying(cc, val) {
			l.latestCC.Store(midiPresent | uint32(cc)<<8 | uint32(val))
			l.latestRawCC.Store(midiPresent | uint32(cc)<<8 | uint32(val))
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

// PeekRawCC reports the most recent raw CC value without consuming it, so the
// calibration UI can show what the fader is actually sending while the normal
// brightness path keeps working.
func (l *Listener) PeekRawCC() (cc, val uint8, ok bool) {
	v := l.latestRawCC.Load()
	if v&midiPresent == 0 {
		return 0, 0, false
	}
	return uint8(v >> 8), uint8(v), true
}

// TakeNote drains one note event. recall reports that it was the configured
// recall key rather than a palette step, so the app does not have to re-derive
// the mapping it was already matched against.
func (l *Listener) TakeNote() (note uint8, viaCC, recall, ok bool) {
	v := l.latestNote.Swap(0)
	if v&midiPresent == 0 {
		return 0, false, false, false
	}
	return uint8(v >> 8), v&midiViaCC != 0, v&midiRecall != 0, true
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
		CC:         uint8(w),
		CCAlt:      uint8(w >> 8),
		NotePlus:   uint8(n),
		NoteMinus:  uint8(n >> 8),
		NoteRecall: uint8(n >> 16),
	}
}

func (l *Listener) Update(cfg config.MIDI) {
	l.storeWanted(cfg)
	SetCCRange(cfg.CCMin, cfg.CCMax)
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

// ccRange holds the calibrated fader endpoints, packed as min<<8|max so both
// move together. Many controllers do not span 0-127: a fader topping out at
// 117 mapped to 92% and the light could never be driven to full.
var ccRange atomic.Uint32

const defaultCCRange = uint32(0)<<8 | 127

func init() { ccRange.Store(defaultCCRange) }

// SetCCRange installs calibrated endpoints. An inverted or collapsed range
// falls back to the full 0-127 span rather than making every move meaningless.
func SetCCRange(min, max uint8) {
	if max <= min {
		ccRange.Store(defaultCCRange)
		return
	}
	ccRange.Store(uint32(min)<<8 | uint32(max))
}

// CCRange reports the calibrated endpoints.
func CCRange() (min, max uint8) {
	v := ccRange.Load()
	return uint8(v >> 8), uint8(v)
}

// CCToPercent maps a raw CC value onto 1-100 across the calibrated travel, so
// a full throw is 100% on any controller.
func CCToPercent(value uint8) int {
	lo, hi := CCRange()
	if value <= lo {
		// Fader bottom is dimmest, never lamp-off. Govee brightness 0 is a
		// power-off on both LAN and BLE (H6001 0x33 0x04 0x00).
		return 1
	}
	if value >= hi {
		return 100
	}
	span := int(hi) - int(lo)
	n := ((int(value)-int(lo))*100 + span/2) / span
	if n < 1 {
		return 1
	}
	if n > 100 {
		return 100
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

// NoteTrigger reports a press of one specific note or CC on any channel. Like
// PaletteTrigger it ignores releases: Note Off, Note On at velocity 0, and CC
// value 0 must not fire, or a single press would act twice.
func NoteTrigger(msg []byte, want uint8) (num uint8, ok bool) {
	if want == 0 || len(msg) < 3 {
		return 0, false
	}
	st := msg[0]
	if st < 0x80 || st >= 0xF0 {
		return 0, false
	}
	n, v := msg[1]&0x7f, msg[2]&0x7f
	if n != want || v == 0 {
		return 0, false
	}
	switch st >> 4 {
	case 0x9: // Note On
		return n, true
	case 0xB: // Control Change — pads that send CC instead of notes
		return n, true
	}
	return 0, false
}

// isBrightnessCC reports whether this message is a Control Change on one of
// the configured brightness CCs. Used to stop a recall note assigned to the
// fader's own number from swallowing every slider move.
func (l *Listener) isBrightnessCC(msg []byte, num uint8) bool {
	if len(msg) < 3 || msg[0]>>4 != 0xB {
		return false
	}
	w := l.ccWanted.Load()
	return num == uint8(w) || num == uint8(w>>8)
}
