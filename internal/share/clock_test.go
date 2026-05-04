package share

import (
	"testing"
	"time"
)

func TestRealClockNow(t *testing.T) {
	c := realClock{}
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("realClock.Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestRealClockTimer(t *testing.T) {
	c := realClock{}
	timer := c.NewTimer(5 * time.Millisecond)
	select {
	case <-timer.C():
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatal("realClock timer did not fire within 500ms")
	}
	if timer.Stop() {
		t.Errorf("Stop() after fire should return false")
	}
}

func TestRealClockTimerStopBeforeFire(t *testing.T) {
	c := realClock{}
	timer := c.NewTimer(1 * time.Hour)
	if !timer.Stop() {
		t.Errorf("Stop() before fire should return true")
	}
}

func TestRealClockTicker(t *testing.T) {
	c := realClock{}
	tick := c.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	got := 0
	timeout := time.After(200 * time.Millisecond)
loop:
	for {
		select {
		case <-tick.C():
			got++
			if got >= 3 {
				break loop
			}
		case <-timeout:
			t.Fatalf("got only %d ticks in 200ms", got)
		}
	}
}
