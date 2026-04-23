package serve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// fakeSession is a programmable share.Session for manager tests.
type fakeSession struct {
	id        string
	mode      string
	reach     string
	ticket    string
	startedAt time.Time
	done      chan struct{}

	stopMu    sync.Mutex
	stopErr   error
	stopCount int
	stopDelay time.Duration
}

func newFakeSession(id string) *fakeSession {
	return &fakeSession{
		id:        id,
		mode:      "tunnel",
		reach:     "https://fake.example",
		ticket:    "fake-ticket",
		startedAt: time.Now(),
		done:      make(chan struct{}),
	}
}

func (f *fakeSession) CredID() string        { return f.id }
func (f *fakeSession) Mode() string          { return f.mode }
func (f *fakeSession) Reach() string         { return f.reach }
func (f *fakeSession) Ticket() string        { return f.ticket }
func (f *fakeSession) StartedAt() time.Time  { return f.startedAt }
func (f *fakeSession) Done() <-chan struct{}  { return f.done }
func (f *fakeSession) Err() error            { return nil }

func (f *fakeSession) Stop() error {
	f.stopMu.Lock()
	f.stopCount++
	first := f.stopCount == 1
	f.stopMu.Unlock()
	if f.stopDelay > 0 {
		time.Sleep(f.stopDelay)
	}
	if first {
		close(f.done)
	}
	return f.stopErr
}

// fakeStarter returns a fakeSession per Start call; errOnStart forces an error.
type fakeStarter struct {
	mu         sync.Mutex
	started    []*fakeSession
	errOnStart error
}

func (s *fakeStarter) StartSession(cred *store.Credential, _ share.Options) (share.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errOnStart != nil {
		return nil, s.errOnStart
	}
	f := newFakeSession(cred.ID)
	s.started = append(s.started, f)
	return f, nil
}

// blockingStarter blocks each StartSession call until unblocked, letting
// tests synchronize concurrent Start calls to exercise the double-check
// path inside Manager.Start.
type blockingStarter struct {
	mu      sync.Mutex
	started []*fakeSession

	// gate is closed by the test to unblock all waiting StartSession calls.
	gate chan struct{}
	// ready is sent to once per StartSession call just before blocking on gate.
	ready chan struct{}
}

func newBlockingStarter() *blockingStarter {
	return &blockingStarter{
		gate:  make(chan struct{}),
		ready: make(chan struct{}, 16),
	}
}

func (s *blockingStarter) StartSession(cred *store.Credential, _ share.Options) (share.Session, error) {
	s.ready <- struct{}{} // signal: reached the gate
	<-s.gate             // wait for test to open the gate
	s.mu.Lock()
	f := newFakeSession(cred.ID)
	s.started = append(s.started, f)
	s.mu.Unlock()
	return f, nil
}

// Test 1 — Start stores the session and Get finds it.
func TestManager_StartStoresSession(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)

	cred := &store.Credential{ID: "cred-1", Name: "one"}
	sess, err := m.Start(cred, share.Options{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess.CredID() != "cred-1" {
		t.Errorf("CredID=%q, want cred-1", sess.CredID())
	}

	got, ok := m.Get("cred-1")
	if !ok || got != sess {
		t.Errorf("Get did not return the stored handle")
	}
}

// Test 2 — Start propagates starter errors.
func TestManager_StartPropagatesStarterError(t *testing.T) {
	starter := &fakeStarter{errOnStart: errors.New("boom")}
	m := NewManager(starter, nil)

	cred := &store.Credential{ID: "cred-1"}
	if _, err := m.Start(cred, share.Options{}); err == nil {
		t.Fatalf("Start succeeded; want error")
	}
}

// Test 3 — Start rejects duplicates.
func TestManager_StartRejectsDuplicate(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	cred := &store.Credential{ID: "dup-1"}
	if _, err := m.Start(cred, share.Options{}); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := m.Start(cred, share.Options{}); err == nil {
		t.Fatalf("second Start succeeded; want ErrAlreadyStarted")
	} else if !errors.Is(err, ErrAlreadyStarted) {
		t.Errorf("err=%v, want ErrAlreadyStarted", err)
	}
}

// Test 4 — Stop removes the session and is idempotent for unknown credIDs.
func TestManager_StopRemovesSession(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	cred := &store.Credential{ID: "cred-1"}
	_, _ = m.Start(cred, share.Options{})

	if err := m.Stop("cred-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := m.Get("cred-1"); ok {
		t.Errorf("Get returned a session after Stop")
	}

	if err := m.Stop("nope"); err != nil {
		t.Errorf("Stop(unknown): %v, want nil", err)
	}
}

// Test 5 — List is sorted by credID.
func TestManager_ListSortedByCredID(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	for _, id := range []string{"c", "a", "b"} {
		_, _ = m.Start(&store.Credential{ID: id}, share.Options{})
	}
	got := m.List()
	if len(got) != 3 {
		t.Fatalf("List len=%d, want 3", len(got))
	}
	if got[0].CredID() != "a" || got[1].CredID() != "b" || got[2].CredID() != "c" {
		t.Errorf("List order = [%s %s %s], want [a b c]",
			got[0].CredID(), got[1].CredID(), got[2].CredID())
	}
}

// Test 6 — Shutdown stops every session in parallel.
func TestManager_ShutdownStopsAllInParallel(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = m.Start(&store.Credential{ID: id}, share.Options{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if len(m.List()) != 0 {
		t.Errorf("List after Shutdown = %d, want 0", len(m.List()))
	}
	for _, s := range starter.started {
		if s.stopCount == 0 {
			t.Errorf("session %s not stopped", s.id)
		}
	}
}

// Test 7 — Shutdown aggregates per-session errors.
func TestManager_ShutdownAggregatesStopErrors(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	sess, _ := m.Start(&store.Credential{ID: "boom"}, share.Options{})
	sess.(*fakeSession).stopErr = errors.New("stop boom")

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatalf("Shutdown err=nil, want aggregate error")
	}
	if !strings.Contains(err.Error(), "stop boom") {
		t.Errorf("err=%v, want to contain 'stop boom'", err)
	}
	if !strings.Contains(err.Error(), "boom") { // credID also appears in aggregate
		t.Errorf("err=%v, want to contain credID", err)
	}
	_ = fmt.Sprint // keep fmt import alive regardless of assertion phrasing
}

// Test 8 — Concurrent Start/Stop for the same credID does not deadlock or panic.
func TestManager_ConcurrentStartStopSameCred(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	cred := &store.Credential{ID: "race-1"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = m.Start(cred, share.Options{}) }()
	go func() { defer wg.Done(); _ = m.Stop("race-1") }()
	wg.Wait()

	// After the race, either the session is gone (Stop ran after Start)
	// or it is present (Start ran after Stop did nothing). Either way
	// the manager must be consistent: Get either returns (sess, true) or
	// (nil, false), never (sess, false) or (nil, true).
	sess, ok := m.Get("race-1")
	if ok && sess == nil {
		t.Errorf("Get returned ok=true with nil session")
	}
	if !ok && sess != nil {
		t.Errorf("Get returned ok=false with non-nil session")
	}

	// Clean up whatever is left.
	_ = m.Shutdown(context.Background())
}

// Test 9 — Start double-check: if two goroutines both pass the pre-check and
// one wins the post-start lock insertion, the loser gets ErrAlreadyStarted.
func TestManager_StartDoubleCheckRejectsLateWinner(t *testing.T) {
	bs := newBlockingStarter()
	m := NewManager(bs, nil)
	cred := &store.Credential{ID: "dbl-1"}

	var (
		wg      sync.WaitGroup
		results [2]error
	)
	// Launch two concurrent Start calls for the same cred.
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, results[0] = m.Start(cred, share.Options{})
	}()
	go func() {
		defer wg.Done()
		_, results[1] = m.Start(cred, share.Options{})
	}()

	// Wait until both goroutines are at the gate (past the pre-check).
	<-bs.ready
	<-bs.ready
	// Open the gate so both proceed to StartSession simultaneously.
	close(bs.gate)
	wg.Wait()

	// Exactly one must succeed and one must return ErrAlreadyStarted.
	successes := 0
	for _, e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, ErrAlreadyStarted) {
			t.Errorf("unexpected error: %v", e)
		}
	}
	if successes != 1 {
		t.Errorf("successes=%d, want exactly 1", successes)
	}
	// Cleanup.
	_ = m.Shutdown(context.Background())
}

// Test 10 — Shutdown timeout: a session whose Stop blocks past the per-session
// deadline produces a timeout error in the aggregate.
func TestManager_ShutdownTimeoutError(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	_, _ = m.Start(&store.Credential{ID: "slow"}, share.Options{})

	// Give the session a long stop delay so the per-session timeout fires.
	starter.started[0].stopDelay = 200 * time.Millisecond

	// Pass an already-cancelled context so the per-session timeout
	// (context.WithTimeout(ctx, 5s)) inherits the cancellation immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel right away

	err := m.Shutdown(ctx)
	if err == nil {
		t.Fatalf("Shutdown err=nil, want timeout aggregate error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("err=%v, want 'timeout'", err)
	}
}

// Test 11 — Shutdown with multiple stop errors produces a "; "-separated
// aggregate, exercising the joinErrs separator branch.
func TestManager_ShutdownMultipleStopErrors(t *testing.T) {
	starter := &fakeStarter{}
	m := NewManager(starter, nil)
	for _, id := range []string{"x1", "x2"} {
		sess, _ := m.Start(&store.Credential{ID: id}, share.Options{})
		sess.(*fakeSession).stopErr = fmt.Errorf("err-%s", id)
	}

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatalf("Shutdown err=nil, want aggregate error")
	}
	// Both errors must appear in the aggregate message.
	if !strings.Contains(err.Error(), "err-x1") {
		t.Errorf("err=%v, want 'err-x1'", err)
	}
	if !strings.Contains(err.Error(), "err-x2") {
		t.Errorf("err=%v, want 'err-x2'", err)
	}
	// The separator "; " must appear when two errors are joined.
	if !strings.Contains(err.Error(), "; ") {
		t.Errorf("err=%v, want '; ' separator between errors", err)
	}
}
