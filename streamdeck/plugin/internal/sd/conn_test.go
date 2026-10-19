package sd

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// pipe returns a Conn whose outbound messages arrive on the returned channel,
// and the Stream Deck end of the socket for sending it events.
func pipe(t *testing.T) (*Conn, *Conn, <-chan map[string]any) {
	t.Helper()
	plugin, deck := net.Pipe()
	t.Cleanup(func() { plugin.Close(); deck.Close() })
	out := make(chan map[string]any, 16)
	server := &Conn{c: deck}
	go func() {
		for {
			b, _, err := server.readFrame()
			if err != nil {
				close(out)
				return
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			out <- m
		}
	}()
	return &Conn{c: plugin}, server, out
}

// sent drains whatever has arrived, giving the reader a moment to catch up.
func sent(out <-chan map[string]any) []string {
	var events []string
	for {
		select {
		case m := <-out:
			events = append(events, m["event"].(string))
		case <-time.After(50 * time.Millisecond):
			return events
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSetImageSkipsRepeats(t *testing.T) {
	c, _, out := pipe(t)
	c.SetImage("k1", "data:a")
	c.SetImage("k1", "data:a")
	c.SetImage("k2", "data:a") // another key is its own record
	c.SetImage("k1", "data:b")
	c.SetImage("k1", "data:a")
	if got, want := sent(out), []string{"setImage", "setImage", "setImage", "setImage"}; !equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
}

func TestSetFeedbackSkipsRepeats(t *testing.T) {
	c, _, out := pipe(t)
	c.SetFeedback("d", map[string]any{"title": "Lights", "value": "3/5"})
	c.SetFeedback("d", map[string]any{"value": "3/5", "title": "Lights"})
	c.SetFeedback("d", map[string]any{"value": "4/5", "title": "Lights"})
	if got, want := sent(out), []string{"setFeedback", "setFeedback"}; !equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
}

// Anything that makes Stream Deck repaint a key itself must let the same image
// or feedback through again, or the key would be left on its default.
func TestRepaintsResendTheSameImage(t *testing.T) {
	c, deck, out := pipe(t)
	c.SetImage("k", "data:a")
	c.SetState("k", 1)
	c.SetImage("k", "data:a")
	c.SetFeedback("d", map[string]any{"strip": "x"})
	c.SetFeedbackLayout("d", "layouts/dial.json")
	c.SetFeedback("d", map[string]any{"strip": "x"})
	want := []string{"setImage", "setState", "setImage", "setFeedback", "setFeedbackLayout", "setFeedback"}
	if got := sent(out); !equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}

	// willAppear arrives from Stream Deck after the key was off screen.
	handled := make(chan struct{})
	go c.Run(func(Event) { close(handled) })
	b, _ := json.Marshal(map[string]any{"event": "willAppear", "context": "k"})
	if err := deck.writeFrame(0x1, b); err != nil {
		t.Fatal(err)
	}
	<-handled
	c.SetImage("k", "data:a")
	if got := sent(out); !equal(got, []string{"setImage"}) {
		t.Fatalf("after willAppear sent %v, want [setImage]", got)
	}
}
