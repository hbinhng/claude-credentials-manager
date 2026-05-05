package share

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// errNoActivated is returned by credPool.Fresh when the pool has no
// active entry — every entry is degraded (multi-pool case) or the
// pool was constructed empty. Callers map this to HTTP 503.
var errNoActivated = errors.New("ccm share: no usable credentials in pool")

// shortID returns up to the first 8 chars of an ID for log lines.
// Real production IDs are UUIDs (36 chars); tests use shorter IDs
// like "a" or "aaaa". This prevents an out-of-range panic when log
// lines run on a short test ID.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

type entryStatus int

const (
	statusCandidate entryStatus = iota
	statusActivated
	statusDegraded
)

func (s entryStatus) String() string {
	switch s {
	case statusCandidate:
		return "candidate"
	case statusActivated:
		return "activated"
	case statusDegraded:
		return "degraded"
	default:
		return "unknown"
	}
}

// poolEntryState abstracts the parts of *credState the pool needs.
// In production it is a *credState; in tests it can be a stub.
type poolEntryState interface {
	tokenSource
	credID() string
	credName() string
	credExpiresAt() time.Time
	credPtr() *store.Credential
}

type poolEntry struct {
	state           poolEntryState
	status          entryStatus
	consecutiveFail int
	lastUsage       *oauth.UsageInfo
	lastUsageAt     time.Time
	lastFeasibility float64
	captured        http.Header // headers captured at last promotion (load-balance mode)
}

// credPool owns the pool of credentials and their per-session
// status. It implements tokenSource — Proxy.tokens points at it in
// load-balance mode.
type credPool struct {
	mu        sync.RWMutex
	entries   map[string]*poolEntry
	activated string
	singleton bool
}

// Compile-time check.
var _ tokenSource = (*credPool)(nil)

// Fresh returns the activated entry's current access token. Returns
// errNoActivated when no entry is currently activated (pool empty
// in the multi-entry "all degraded" sense).
func (p *credPool) Fresh() (string, error) {
	p.mu.RLock()
	if p.activated == "" {
		p.mu.RUnlock()
		return "", errNoActivated
	}
	state := p.entries[p.activated].state
	p.mu.RUnlock()
	return state.Fresh()
}

// MarkProbe records the result of one usage probe against an entry
// and applies the per-entry state-machine rules.
//
// MarkProbe NEVER demotes the activated entry — only the scheduler
// can do that, via Demote. This is intentional: rotation is a
// scheduler-policy decision, not a probe-side-effect.
func (p *credPool) MarkProbe(id string, info *oauth.UsageInfo, err error) {
	p.mu.Lock()
	e, ok := p.entries[id]
	if !ok {
		p.mu.Unlock()
		return
	}
	prevStatus := e.status
	if err == nil {
		e.consecutiveFail = 0
		e.lastUsage = info
		e.lastUsageAt = time.Now()
		if e.status == statusDegraded {
			e.status = statusCandidate
		}
	} else {
		e.consecutiveFail++
		if e.status == statusCandidate && e.consecutiveFail >= 2 {
			e.status = statusDegraded
		}
	}
	newStatus := e.status
	name := e.state.credName()
	p.mu.Unlock()

	// Emit transition logs after releasing the lock.
	if prevStatus == statusCandidate && newStatus == statusDegraded {
		fmt.Fprintf(errLog(), "ccm share: %s(%s) degraded after 2 failures: %v\n", name, shortID(id), err)
	}
	if prevStatus == statusDegraded && newStatus == statusCandidate {
		fmt.Fprintf(errLog(), "ccm share: %s(%s) recovered, back in pool\n", name, shortID(id))
	}
}

// Promote atomically swaps the activated entry to newID and stores
// the captured headers for the new activated. Headers are cloned on
// store so callers can pass a value they later mutate.
//
// The old activated is demoted to degraded if its consecutiveFail >= 2
// (counter preserved); otherwise to candidate with the counter reset
// to 0 (rotation itself is the recovery signal for a healthy loser).
func (p *credPool) Promote(newID string, headers http.Header) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.entries[p.activated]; ok {
		if old.consecutiveFail >= 2 {
			old.status = statusDegraded
		} else {
			old.status = statusCandidate
			old.consecutiveFail = 0
		}
	}
	if e, ok := p.entries[newID]; ok {
		e.status = statusActivated
		e.consecutiveFail = 0
		if headers != nil {
			e.captured = headers.Clone()
		}
	}
	p.activated = newID
}

// activatedHeaders returns the activated entry's captured headers
// (or nil if no entry is currently activated). Used by Proxy.director
// in load-balance mode to replay the right per-cred headers.
//
// Returns the stored slice without further cloning — director clones
// values per key into the outbound request, and Promote already
// cloned on store. The returned http.Header must be treated as
// read-only by callers.
func (p *credPool) activatedHeaders() http.Header {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.activated == "" {
		return nil
	}
	e, ok := p.entries[p.activated]
	if !ok {
		return nil
	}
	return e.captured
}

// Demote clears the activated slot — Fresh() will return
// errNoActivated until a future Promote happens. Caller must
// guarantee !p.singleton; we panic to surface the invariant
// violation in tests.
func (p *credPool) Demote(oldID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.singleton {
		panic("ccm share: Demote called on singleton pool — invariant violation")
	}
	if e, ok := p.entries[oldID]; ok {
		e.status = statusDegraded
	}
	p.activated = ""
}

// SignalActivatedFailed bumps the activated entry's
// consecutiveFail. The next scheduler tick reads the counter and,
// if it has reached the threshold, may demote the activated.
// No-op when no entry is currently activated.
func (p *credPool) SignalActivatedFailed() {
	p.mu.Lock()
	if p.activated == "" {
		p.mu.Unlock()
		return
	}
	e, ok := p.entries[p.activated]
	if !ok {
		p.mu.Unlock()
		return
	}
	e.consecutiveFail++
	count := e.consecutiveFail
	name := e.state.credName()
	id := p.activated
	p.mu.Unlock()
	fmt.Fprintf(errLog(), "ccm share: upstream 401 on activated %s(%s) (failure %d/2)\n",
		name, shortID(id), count)
}

// PoolEntryView is a read-only snapshot of one pool entry's state.
type PoolEntryView struct {
	id              string
	name            string
	status          entryStatus
	consecutiveFail int
	lastFeasibility float64
	lastUsageAt     time.Time
}

// Snapshot returns a deep copy of every entry's current state, for
// logging and SIGUSR1 introspection. Mutations to the returned
// slice do not affect pool state.
func (p *credPool) Snapshot() []PoolEntryView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PoolEntryView, 0, len(p.entries))
	for id, e := range p.entries {
		out = append(out, PoolEntryView{
			id:              id,
			name:            e.state.credName(),
			status:          e.status,
			consecutiveFail: e.consecutiveFail,
			lastFeasibility: e.lastFeasibility,
			lastUsageAt:     e.lastUsageAt,
		})
	}
	return out
}

// PoolReader is the read-only surface exposed by Session.Pool().
// Used by cmd/share for SIGUSR1 dumps without leaking unexported
// pool internals.
type PoolReader interface {
	SnapshotLines() []string
}

// SnapshotLines renders one log-friendly line per entry. Reads
// directly from p.entries under p.mu.RLock — does not call Snapshot
// (which takes its own RLock; sync.RWMutex does not allow recursive
// RLocks).
func (p *credPool) SnapshotLines() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.entries))
	for id, e := range p.entries {
		last := "never"
		if !e.lastUsageAt.IsZero() {
			last = e.lastUsageAt.Format(time.RFC3339)
		}
		name := e.state.credName()
		if name == "" {
			name = shortID(id)
		} else {
			name = fmt.Sprintf("%s(%s)", name, shortID(id))
		}
		hdrs := "unset"
		if e.captured != nil {
			hdrs = fmt.Sprintf("%d", len(e.captured))
		}
		out = append(out, fmt.Sprintf("  %s status=%s fail=%d feasibility=%.3f last=%s headers=%s",
			name, e.status, e.consecutiveFail, e.lastFeasibility, last, hdrs))
	}
	return out
}
