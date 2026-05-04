package share

import (
	"sync"
	"time"
)

// clock is the test seam for time-dependent share components
// (scheduler tick, refresh timers). Production wiring uses
// realClock; tests pass a *fakeClock.
type clock interface {
	Now() time.Time
	NewTimer(d time.Duration) clockTimer
	NewTicker(d time.Duration) clockTicker
}

type clockTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type clockTicker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) clockTimer {
	return realTimer{t: time.NewTimer(d)}
}

func (realClock) NewTicker(d time.Duration) clockTicker {
	return realTicker{t: time.NewTicker(d)}
}

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	tickers []*fakeTicker
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	timers := append([]*fakeTimer(nil), f.timers...)
	tickers := append([]*fakeTicker(nil), f.tickers...)
	f.mu.Unlock()

	for _, t := range timers {
		t.maybeFire(now)
	}
	for _, t := range tickers {
		t.maybeFire(now)
	}
}

func (f *fakeClock) NewTimer(d time.Duration) clockTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{ch: make(chan time.Time, 1), fireAt: f.now.Add(d)}
	f.timers = append(f.timers, t)
	return t
}

func (f *fakeClock) NewTicker(d time.Duration) clockTicker {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTicker{ch: make(chan time.Time, 1), interval: d, nextFire: f.now.Add(d)}
	f.tickers = append(f.tickers, t)
	return t
}

type fakeTimer struct {
	mu      sync.Mutex
	ch      chan time.Time
	fireAt  time.Time
	fired   bool
	stopped bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (t *fakeTimer) maybeFire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired || t.stopped || now.Before(t.fireAt) {
		return
	}
	t.fired = true
	select {
	case t.ch <- now:
	default:
	}
}

type fakeTicker struct {
	mu       sync.Mutex
	ch       chan time.Time
	interval time.Duration
	nextFire time.Time
	stopped  bool
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }

func (t *fakeTicker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
}

func (t *fakeTicker) maybeFire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	for !now.Before(t.nextFire) {
		select {
		case t.ch <- t.nextFire:
		default:
			// Drop tick if receiver hasn't drained — matches
			// time.Ticker semantics.
		}
		t.nextFire = t.nextFire.Add(t.interval)
	}
}
