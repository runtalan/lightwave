// Package sd implements the Stream Deck plugin WebSocket protocol.
//
// Stream Deck launches the plugin with -port/-pluginUUID/-registerEvent and
// expects a WebSocket connection to 127.0.0.1 that opens with a registration
// message. Only a tiny slice of RFC 6455 is needed for a localhost text
// channel, so this is implemented directly rather than pulling in a dependency.
package sd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
)

// Event is an inbound Stream Deck message.
type Event struct {
	Event   string  `json:"event"`
	Action  string  `json:"action"`
	Context string  `json:"context"`
	Device  string  `json:"device"`
	Payload Payload `json:"payload"`
}

type Payload struct {
	Settings json.RawMessage `json:"settings"`
	Ticks    int             `json:"ticks"`
	Pressed  bool            `json:"pressed"`
	State    int             `json:"state"`
	// Title carries what Stream Deck is currently showing on the key, and
	// TitleParameters.ShowTitle whether the user has the title enabled. Both
	// arrive with titleParametersDidChange, which is how the plugin learns the
	// user has typed their own label and stops overwriting it.
	Title           string `json:"title"`
	TitleParameters struct {
		ShowTitle bool `json:"showTitle"`
	} `json:"titleParameters"`
}

type Conn struct {
	c  net.Conn
	mu sync.Mutex // serializes frame writes
}

// Dial connects to Stream Deck and registers the plugin.
func Dial(port int, pluginUUID, registerEvent string) (*Conn, error) {
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		c.Close()
		return nil, err
	}
	k := base64.StdEncoding.EncodeToString(key)
	req := "GET / HTTP/1.1\r\n" +
		fmt.Sprintf("Host: 127.0.0.1:%d\r\n", port) +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + k + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		c.Close()
		return nil, err
	}
	if err := readHandshake(c); err != nil {
		c.Close()
		return nil, err
	}
	conn := &Conn{c: c}
	reg := map[string]string{"event": registerEvent, "uuid": pluginUUID}
	if err := conn.send(reg); err != nil {
		c.Close()
		return nil, err
	}
	log.Printf("registered with Stream Deck")
	return conn, nil
}

func readHandshake(c net.Conn) error {
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1)
	for {
		n, err := c.Read(tmp)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		buf = append(buf, tmp[0])
		if len(buf) >= 4 && string(buf[len(buf)-4:]) == "\r\n\r\n" {
			break
		}
		if len(buf) > 8192 {
			return fmt.Errorf("handshake too large")
		}
	}
	head := string(buf)
	if !strings.Contains(head, "101") {
		return fmt.Errorf("websocket upgrade refused: %s", strings.SplitN(head, "\r\n", 2)[0])
	}
	return nil
}

// Run reads frames until the connection closes, dispatching to handler.
func (w *Conn) Run(handler func(Event)) {
	defer w.c.Close()
	for {
		payload, opcode, err := w.readFrame()
		if err != nil {
			if err != io.EOF {
				log.Printf("websocket read: %v", err)
			}
			return
		}
		switch opcode {
		case 0x8: // close
			return
		case 0x9: // ping -> pong
			_ = w.writeFrame(0xA, payload)
			continue
		case 0x1, 0x2:
		default:
			continue
		}
		var ev Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("handler %s recovered: %v", ev.Event, r)
				}
			}()
			handler(ev)
		}()
	}
}

func (w *Conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.writeFrame(0x1, b)
}

// writeFrame writes a client frame. Client->server frames must be masked.
func (w *Conn) writeFrame(opcode byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var hdr []byte
	n := len(payload)
	switch {
	case n < 126:
		hdr = []byte{0x80 | opcode, byte(0x80 | n)}
	case n <= 0xFFFF:
		hdr = make([]byte, 4)
		hdr[0], hdr[1] = 0x80|opcode, 0x80|126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0], hdr[1] = 0x80|opcode, 0x80|127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	frame := append(append(hdr, mask...), masked...)
	_, err := w.c.Write(frame)
	return err
}

func (w *Conn) readFrame() ([]byte, byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(w.c, h[:]); err != nil {
		return nil, 0, err
	}
	opcode := h[0] & 0x0F
	masked := h[1]&0x80 != 0
	length := uint64(h[1] & 0x7F)
	switch length {
	case 126:
		var e [2]byte
		if _, err := io.ReadFull(w.c, e[:]); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(e[:]))
	case 127:
		var e [8]byte
		if _, err := io.ReadFull(w.c, e[:]); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(e[:])
	}
	if length > 1<<22 {
		return nil, 0, fmt.Errorf("frame too large: %d", length)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(w.c, mask[:]); err != nil {
			return nil, 0, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(w.c, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, opcode, nil
}

// Outbound API.

func (w *Conn) SetTitle(context, title string) {
	_ = w.send(map[string]any{
		"event":   "setTitle",
		"context": context,
		"payload": map[string]any{"title": title, "target": 0},
	})
}

// SetImage sets a key's image. data must be a data: URI (base64 PNG).
func (w *Conn) SetImage(context, data string) {
	_ = w.send(map[string]any{
		"event":   "setImage",
		"context": context,
		"payload": map[string]any{"image": data, "target": 0},
	})
}

func (w *Conn) SetState(context string, state int) {
	_ = w.send(map[string]any{
		"event":   "setState",
		"context": context,
		"payload": map[string]any{"state": state},
	})
}

func (w *Conn) ShowAlert(context string) {
	_ = w.send(map[string]any{"event": "showAlert", "context": context})
}

func (w *Conn) ShowOK(context string) {
	_ = w.send(map[string]any{"event": "showOk", "context": context})
}

func (w *Conn) SetSettings(context string, v any) {
	_ = w.send(map[string]any{"event": "setSettings", "context": context, "payload": v})
}

// SendToPropertyInspector pushes a payload to the open Property Inspector for
// this action. The inspector is a sandboxed web view with no route to
// Lightwave's socket, so anything it needs from the daemon — the bound lights
// and their names — has to arrive this way.
func (w *Conn) SendToPropertyInspector(context, action string, v any) {
	_ = w.send(map[string]any{
		"event":   "sendToPropertyInspector",
		"context": context,
		"action":  action,
		"payload": v,
	})
}
