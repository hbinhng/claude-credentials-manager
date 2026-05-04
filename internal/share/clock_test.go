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

func TestFakeClockAdvance(t *testing.T) {
	fc := newFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	if got := fc.Now(); got.Hour() != 12 {
		t.Errorf("initial Now hour = %d, want 12", got.Hour())
	}
	fc.Advance(2 * time.Hour)
	if got := fc.Now(); got.Hour() != 14 {
		t.Errorf("after Advance Now hour = %d, want 14", got.Hour())
	}
}

func TestFakeClockTimerFiresOnAdvance(t *testing.T) {
	fc := newFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	timer := fc.NewTimer(10 * time.Second)

	select {
	case <-timer.C():
		t.Fatal("timer fired before Advance")
	default:
	}

	fc.Advance(10 * time.Second)
	select {
	case fired := <-timer.C():
		if fired.Sub(fc.Now()) != 0 {
			t.Errorf("timer fired with wrong stamp: %v", fired)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timer did not fire after Advance")
	}
}

func TestFakeClockTimerStopBeforeFire(t *testing.T) {
	fc := newFakeClock(time.Now())
	timer := fc.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Errorf("Stop() before fire should return true")
	}
	fc.Advance(2 * time.Hour)
	select {
	case <-timer.C():
		t.Fatal("timer fired after Stop")
	default:
	}
}

func TestFakeClockTickerFiresEachInterval(t *testing.T) {
	fc := newFakeClock(time.Now())
	tick := fc.NewTicker(5 * time.Second)
	defer tick.Stop()

	fc.Advance(5 * time.Second)
	select {
	case <-tick.C():
	default:
		t.Fatal("tick 1 missing")
	}

	fc.Advance(5 * time.Second)
	select {
	case <-tick.C():
	default:
		t.Fatal("tick 2 missing")
	}
}
