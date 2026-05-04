package share

import (
	"errors"
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
	defer p.mu.Unlock()
	e, ok := p.entries[id]
	if !ok {
		return
	}
	if err == nil {
		e.consecutiveFail = 0
		e.lastUsage = info
		e.lastUsageAt = time.Now()
		if e.status == statusDegraded {
			e.status = statusCandidate
		}
		return
	}
	e.consecutiveFail++
	if e.status == statusCandidate && e.consecutiveFail >= 2 {
		e.status = statusDegraded
	}
}
