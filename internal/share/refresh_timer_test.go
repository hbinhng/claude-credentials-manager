package share

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func credWithExpiry(at time.Time) *store.Credential {
	return &store.Credential{
		ID: "00000000-0000-0000-0000-000000000001",
		ClaudeAiOauth: store.OAuthTokens{
			ExpiresAt: at.UnixMilli(),
		},
	}
}

func TestNextRefreshDelayAlreadyExpired(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	cred := credWithExpiry(now.Add(-time.Hour))
	got := nextRefreshDelay(cred, now, func() time.Duration { return 30 * time.Second })
	if got != 0 {
		t.Errorf("got %v, want 0 (jitter clamped to delay/4 == 0)", got)
	}
}

func TestNextRefreshDelay30MinFuture(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	cred := credWithExpiry(now.Add(30 * time.Minute))
	jitter := 30 * time.Second
	got := nextRefreshDelay(cred, now, func() time.Duration { return jitter })
	// target = 25 min; delay = 25 min; jitter offered 30s; cap min(60s, delay/4=6.25m)=60s; final = 30s
	want := 25*time.Minute + 30*time.Second
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNextRefreshDelayJitterClampedByDelay(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	// Token expires in 5m10s ⇒ target 10s away; delay/4 = 2.5s
	cred := credWithExpiry(now.Add(5*time.Minute + 10*time.Second))
	got := nextRefreshDelay(cred, now, func() time.Duration { return 60 * time.Second })
	want := 10*time.Second + 2500*time.Millisecond
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNextRefreshDelayNegativeJitter(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	cred := credWithExpiry(now.Add(time.Hour))
	got := nextRefreshDelay(cred, now, func() time.Duration { return -42 * time.Second })
	want := 55 * time.Minute
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNextRefreshDelayJitterCappedAt60s(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	// Expires in 24 hours ⇒ delay = 23h55m; delay/4 ≈ 5h59m; jitter cap is min(60s, that) = 60s
	cred := credWithExpiry(now.Add(24 * time.Hour))
	got := nextRefreshDelay(cred, now, func() time.Duration { return 5 * time.Minute })
	want := 23*time.Hour + 55*time.Minute + 60*time.Second
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

// fakeRefreshableState lets the refresh timer be unit-tested without
// the OAuth machinery. It pretends to be a poolEntryState whose
// Fresh() either returns a stub error or bumps the credential's
// ExpiresAt (simulating a successful refresh).
type fakeRefreshableState struct {
	id        string
	expiresAt int64 // unix milli
	failNext  atomic.Bool
	calls     atomic.Int32
}

func (f *fakeRefreshableState) Fresh() (string, error) {
	f.calls.Add(1)
	if f.failNext.Load() {
		return "", fmt.Errorf("refresh failed")
	}
	f.expiresAt = time.Now().Add(8 * time.Hour).UnixMilli()
	return "tok", nil
}
func (f *fakeRefreshableState) credID() string           { return f.id }
func (f *fakeRefreshableState) credName() string         { return "" }
func (f *fakeRefreshableState) credExpiresAt() time.Time { return time.UnixMilli(f.expiresAt) }
func (f *fakeRefreshableState) credPtr() *store.Credential {
	return &store.Credential{ID: f.id, ClaudeAiOauth: store.OAuthTokens{ExpiresAt: f.expiresAt}}
}

func TestRefreshTimerExitsOnDoneClose(t *testing.T) {
	state := &fakeRefreshableState{id: "a", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	fc := newFakeClock(time.Now())
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		runRefreshTimer(state, fc, func() time.Duration { return 0 }, done)
		close(exited)
	}()
	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("refresh timer did not exit after done closed")
	}
}

func TestRefreshTimerCallsFreshWhenTimerFires(t *testing.T) {
	now := time.Now()
	state := &fakeRefreshableState{id: "a", expiresAt: now.Add(10 * time.Minute).UnixMilli()}
	fc := newFakeClock(now)
	done := make(chan struct{})
	defer close(done)
	go runRefreshTimer(state, fc, func() time.Duration { return 0 }, done)

	// Wait for the goroutine to register its timer (small spin).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		registered := len(fc.timers) > 0
		fc.mu.Unlock()
		if registered {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Advance past 5min margin so the timer fires.
	fc.Advance(6 * time.Minute)

	// Wait for Fresh to be invoked.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if state.calls.Load() >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := state.calls.Load(); got < 1 {
		t.Errorf("Fresh calls = %d, want >= 1", got)
	}
}
