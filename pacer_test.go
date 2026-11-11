package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A single call runs before kick returns, so one press lands at once.
func TestPacerRunsALoneCallAtOnce(t *testing.T) {
	var p pacer
	ran := false
	p.kick(50*time.Millisecond, func() { ran = true })
	if !ran {
		t.Fatal("an idle pacer must run the first call on the caller")
	}
}

// A burst folds into a few runs, and the last one sees the final value.
func TestPacerFoldsABurstAndEndsOnTheLatest(t *testing.T) {
	var p pacer
	var mu sync.Mutex
	var runs atomic.Int32
	latest, seen := 0, 0
	run := func() {
		runs.Add(1)
		mu.Lock()
		seen = latest
		mu.Unlock()
	}
	const every = 40 * time.Millisecond
	start := time.Now()
	for i := 1; i <= 50; i++ {
		mu.Lock()
		latest = i
		mu.Unlock()
		p.kick(every, run)
		time.Sleep(2 * time.Millisecond)
	}
	burst := time.Since(start)
	time.Sleep(3 * every)
	mu.Lock()
	defer mu.Unlock()
	if seen != 50 {
		t.Fatalf("last run saw %d, want the final value 50", seen)
	}
	// One leading run, one per interval through the burst, one trailing.
	if max := int32(burst/every) + 3; runs.Load() > max {
		t.Fatalf("%d runs for a %v burst, want at most %d", runs.Load(), burst, max)
	}
}
