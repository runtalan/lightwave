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
	name := in.String()
	for _, p := range ins {
		s := p.String()
		ls := strings.ToLower(s)
		if strings.Contains(ls, "glorious") || strings.Contains(ls, "gmmk") || strings.Contains(ls, "numpad") {
			in = p
			name = s
			break
		}
	}

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
	log.Printf("midi: listening on %s (CC %d/%d, notes +%d -%d)", name, cfg.CC, cfg.CCAlt, cfg.NotePlus, cfg.NoteMinus)
	l.setStatus(true, name)
	return nil
}

// onMIDI runs on the CoreMIDI / RtMidi CGO thread.
// Store numbers only. Never lock, log, emit, or send on a channel.
func (l *Listener) onMIDI(msg gomidi.Message, _ int32) {
	defer func() { _ = recover() }()

	var ch, cc, val uint8
	if msg.GetControlChange(&ch, &cc, &val) {
		want := l.ccWanted.Load()
		if cc == uint8(want) || cc == uint8(want>>8) {
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
	var key, vel uint8
	if msg.GetNoteOn(&ch, &key, &vel) && vel > 0 {
		want := l.noteWanted.Load()
		if key == uint8(want) || key == uint8(want>>8) {
			l.latestNote.Store(midiPresent | uint32(key))
		}
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
	return uint8(v >> 8), uint8(v), true
}

func (l *Listener) TakeNote() (note uint8, ok bool) {
	v := l.latestNote.Swap(0)
	if v&midiPresent == 0 {
		return 0, false
	}
	return uint8(v), true
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
	return int((float64(value) / 127.0) * 100.0)
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
