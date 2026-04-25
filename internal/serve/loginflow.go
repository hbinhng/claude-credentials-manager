package serve

import (
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
)

// loginHandshakeTTL is how long a half-finished OAuth handshake is
// kept before a sweep prunes it. Ten minutes covers a user who opens
// the dialog, walks away to read mail, and comes back — but is short
// enough that an abandoned handshake doesn't pile up indefinitely.
const loginHandshakeTTL = 10 * time.Minute

// loginHandshakeSweepInterval is the cadence at which the sweeper
// goroutine walks the map to evict expired entries. Peek itself does
// a TTL check, so the sweeper only exists to bound the live map size.
const loginHandshakeSweepInterval = time.Minute

// handshakeStore is an in-process registry of in-flight OAuth login
// handshakes. The dashboard hands the user back the entry's ID;
// finishing the dialog Peeks (and on success, Deletes) by that ID.
//
// The store is owned by a single ccm serve process and is never
// persisted — restarting the server invalidates every entry, which is
// fine: a half-finished login is cheap to retry.
type handshakeStore struct {
	mu      sync.Mutex
	entries map[string]*handshakeEntry
	now     func() time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

type handshakeEntry struct {
	h         *credflow.Handshake
	createdAt time.Time
}

// newHandshakeStore returns a ready store with its sweeper goroutine
// already running. Callers must Close it to stop the goroutine.
func newHandshakeStore() *handshakeStore {
	s := &handshakeStore{
		entries: make(map[string]*handshakeEntry),
		now:     time.Now,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go s.runSweeper(loginHandshakeSweepInterval)
	return s
}

// Put records a fresh handshake under its own ID.
func (s *handshakeStore) Put(h *credflow.Handshake) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[h.ID] = &handshakeEntry{h: h, createdAt: s.now()}
}

// Peek returns the handshake registered under id without consuming
// it. Entries older than loginHandshakeTTL are evicted lazily and
// reported as missing — callers don't need to distinguish "expired"
// from "never existed", because either case demands the user starts
// over.
func (s *handshakeStore) Peek(id string) (*credflow.Handshake, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return nil, false
	}
	if s.now().Sub(e.createdAt) > loginHandshakeTTL {
		delete(s.entries, id)
		return nil, false
	}
	return e.h, true
}

// Delete removes the handshake under id. Safe to call on a missing
// id; the call is a no-op in that case.
func (s *handshakeStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
}

// Close stops the sweeper goroutine and waits for it to exit. Safe
// to call multiple times; only the first call has effect.
func (s *handshakeStore) Close() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		<-s.doneCh
	})
}

// runSweeper ticks every interval and prunes expired entries.
// Exported parameter (rather than the package const) lets the test
// suite drive the loop quickly.
func (s *handshakeStore) runSweeper(interval time.Duration) {
	defer close(s.doneCh)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.sweep(s.now())
		}
	}
}

// sweep walks the entries map and deletes anything older than
// loginHandshakeTTL relative to the supplied now. Pure given (now,
// entries); tests call it directly with an injected timestamp.
func (s *handshakeStore) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.entries {
		if now.Sub(e.createdAt) > loginHandshakeTTL {
			delete(s.entries, id)
		}
	}
}
