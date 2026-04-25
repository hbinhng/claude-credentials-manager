package serve

import (
	"sync"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
)

// fakeClock is a goroutine-safe time source the tests hand to a
// handshakeStore at construction. Reassigning s.now after the
// sweeper has started would race; this lets the sweeper read the
// same closure throughout the test's life.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// newStoreWithClock builds a handshakeStore wired to clock.get and
// kicks off its sweeper at the supplied interval. Mirrors what
// newHandshakeStore does so the production happy-path stays tested
// elsewhere.
func newStoreWithClock(clock *fakeClock, sweepInterval time.Duration) *handshakeStore {
	s := &handshakeStore{
		entries: make(map[string]*handshakeEntry),
		now:     clock.get,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go s.runSweeper(sweepInterval)
	return s
}

func TestHandshakeStore_PutPeekDelete(t *testing.T) {
	s := newHandshakeStore()
	defer s.Close()

	hs := &credflow.Handshake{ID: "abc", AuthorizeURL: "u"}
	s.Put(hs)

	got, ok := s.Peek("abc")
	if !ok {
		t.Fatalf("Peek miss after Put")
	}
	if got != hs {
		t.Errorf("Peek returned different handshake")
	}
	// Peek does not consume.
	if _, ok := s.Peek("abc"); !ok {
		t.Errorf("Peek consumed the entry")
	}

	s.Delete("abc")
	if _, ok := s.Peek("abc"); ok {
		t.Errorf("entry still present after Delete")
	}
	// Delete on missing id is a no-op.
	s.Delete("nope")
}

func TestHandshakeStore_PeekUnknown(t *testing.T) {
	s := newHandshakeStore()
	defer s.Close()
	if _, ok := s.Peek("never-stored"); ok {
		t.Errorf("Peek of unknown id returned ok=true")
	}
}

func TestHandshakeStore_PeekTTLEvict(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	// Long sweep interval so only Peek's lazy eviction is exercised.
	s := newStoreWithClock(clock, time.Hour)
	defer s.Close()

	s.Put(&credflow.Handshake{ID: "x"})

	clock.set(clock.get().Add(loginHandshakeTTL + time.Second))
	if _, ok := s.Peek("x"); ok {
		t.Errorf("expired entry returned ok=true from Peek")
	}
	s.mu.Lock()
	_, present := s.entries["x"]
	s.mu.Unlock()
	if present {
		t.Errorf("expired entry not evicted by Peek")
	}
}

func TestHandshakeStore_SweepRemovesExpired(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	s := newStoreWithClock(clock, time.Hour)
	defer s.Close()

	start := clock.get()
	s.Put(&credflow.Handshake{ID: "old"})
	clock.set(start.Add(loginHandshakeTTL / 2))
	s.Put(&credflow.Handshake{ID: "fresh"})

	clock.set(start.Add(loginHandshakeTTL + time.Second))
	s.sweep(clock.get())

	if _, ok := s.Peek("old"); ok {
		t.Errorf("'old' survived sweep")
	}
	if _, ok := s.Peek("fresh"); !ok {
		t.Errorf("'fresh' was evicted prematurely")
	}
}

func TestHandshakeStore_CloseStopsSweeper(t *testing.T) {
	s := newHandshakeStore()
	s.Close()
	select {
	case <-s.doneCh:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatalf("sweeper did not exit within 2s of Close")
	}
}

func TestHandshakeStore_CloseIdempotent(t *testing.T) {
	s := newHandshakeStore()
	s.Close()
	s.Close() // must not panic or block
}

func TestHandshakeStore_SweeperFires(t *testing.T) {
	// Tight 5ms interval keeps the test quick. Put first under T0,
	// then advance the fake clock past TTL so the next sweep tick
	// observes the entry as expired.
	t0 := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: t0}
	s := newStoreWithClock(clock, 5*time.Millisecond)
	defer s.Close()

	s.Put(&credflow.Handshake{ID: "stale"})
	clock.set(t0.Add(loginHandshakeTTL + time.Second))

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, present := s.entries["stale"]
		s.mu.Unlock()
		if !present {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sweeper never evicted the stale entry within 500ms")
}
