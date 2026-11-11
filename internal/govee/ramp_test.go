package govee

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A pool ramps side by side: nine lamps take as long as one, and each still
// steps up to its own target.
func TestPoolBrightnessRampsLampsTogether(t *testing.T) {
	var mu sync.Mutex
	last := map[string]string{}
	writes := map[string]int{}
	SetTestControlSink(func(ip, p string) {
		mu.Lock()
		last[ip] = p
		writes[ip]++
		mu.Unlock()
	})
	t.Cleanup(func() { SetTestControlSink(nil) })

	targets := map[string]int{}
	for i := 0; i < 9; i++ {
		ip := "192.0.2." + strconv.Itoa(100+i)
		lastBright.Store(ip, 20)
		targets[ip] = 90 - i
	}
	start := time.Now()
	ApplyPoolBrightnessMap(targets, false)
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("nine lamps took %v; the ramps should run together (~60ms)", took)
	}
	for ip, want := range targets {
		if writes[ip] != 4 {
			t.Fatalf("%s got %d writes, want a 4-step ramp", ip, writes[ip])
		}
		if !strings.Contains(last[ip], `"value":`+strconv.Itoa(want)+`}`) {
			t.Fatalf("%s ended on %s, want %d", ip, last[ip], want)
		}
	}
}
