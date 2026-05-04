package share

import "time"

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
