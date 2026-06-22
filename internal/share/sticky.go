package share

import (
	"fmt"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/usage"
)

// Sticky-mode tunables (none configurable in v1):
//   - stickyIdleTTL: a pin with no request in this long is swept — the
//     session went away.
//   - stickyCacheGraceTTL: a pin with no SUCCESSFUL response in this long
//     is swept — the prompt cache (~1h TTL) has gone cold, so re-pinning
//     costs nothing. Bounds how long a session sticks to a persistently-
//     failing credential (transient 5xx / rate-limit / transport error).
//   - stickyExhaustionUtilization: Quota.Used (0..100) at/above this on a
//     429 marks a usage-cap (quota-exhausted) 429 vs a transient burst.
//     Matches ccm's existing "exhausted" semantics; isolated here so it is
//     a one-line retune if real 429 utilization values differ.
const (
	stickyIdleTTL               = 30 * time.Minute
	stickyCacheGraceTTL         = 1 * time.Hour
	stickyExhaustionUtilization = 99.0
)

// sessionPin records which pool entry a Claude Code session is bound to,
// when it was last seen (any request — drives idle eviction), and when it
// last got a successful response (drives cache-grace eviction).
type sessionPin struct {
	entryID     string
	lastSeen    time.Time
	lastSuccess time.Time
}

// enableSticky switches the pool to per-session sticky routing:
//   - clears the global activated slot (no single active entry),
//   - demotes the previously-activated entry back to candidate so the
//     uniform candidate-degrade/recover machinery applies to every entry,
//   - derives the shared local install-identity headers from whichever
//     entry BuildPool captured (reused for every local entry), and
//   - records the clock used for pin timestamps and idle eviction.
func (p *credPool) enableSticky(clk clock) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sticky = true
	p.sessions = make(map[string]*sessionPin)
	p.clk = clk
	p.activated = ""
	for _, e := range p.entries {
		if e.captured != nil && p.localCaptured == nil {
			p.localCaptured = e.captured
		}
	}
	for _, e := range p.entries {
		if e.status == statusActivated {
			e.status = statusCandidate
		}
	}
}

// now returns the pool's current time from its sticky clock, falling
// back to realClock if none was set.
func (p *credPool) now() time.Time {
	if p.clk != nil {
		return p.clk.Now()
	}
	// coverage: unreachable in production — enableSticky always sets clk
	// before any sticky method runs. Defensive guard for direct callers.
	return realClock{}.Now()
}

// bestCandidate returns the highest-feasibility eligible entry id.
// Eligible = not degraded AND consecutiveFail < 2. ok=false when none
// qualify (caller maps to 503).
func (p *credPool) bestCandidate() (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bestCandidateLocked()
}

// bestCandidateLocked is bestCandidate's body; caller holds p.mu (R or W).
func (p *credPool) bestCandidateLocked() (string, bool) {
	bestID := ""
	var bestFeas float64
	for id, e := range p.entries {
		// New-session / re-pin selection requires a CLEAN record: exclude
		// degraded entries AND any entry with an unresolved failure
		// (consecutiveFail > 0). Pin RETENTION (routeSession's reuse check)
		// stays at the looser fail < 2 so an already-pinned session rides
		// through a transient blip. consecutiveFail resets to 0 on a
		// successful probe / response, re-admitting the entry.
		if e.status == statusDegraded || e.consecutiveFail > 0 {
			continue
		}
		f := e.lastFeasibility
		if bestID == "" || f > bestFeas || (f == bestFeas && id < bestID) {
			bestID, bestFeas = id, f
		}
	}
	return bestID, bestID != ""
}

// viewForEntryLocked builds the per-request director snapshot for entry
// id (which the caller guarantees exists) and returns the entry's
// token source for an out-of-lock refresh. Local entries replay the
// shared install identity; passthrough entries forward as-is.
func (p *credPool) viewForEntryLocked(id string) (activatedView, string, tokenSource, error) {
	e := p.entries[id]
	v := activatedView{
		upstreamURL:   e.state.upstreamURL(),
		isPassthrough: e.state.isPassthrough(),
		ok:            true,
	}
	if !e.state.isPassthrough() {
		v.captured = p.localCaptured
	}
	return v, id, e.state, nil
}

// signalEntryFailed records a 401 (dead-token) hard failure for entry
// entryID and drops the session's pin so the session's next request
// re-selects. The entry's consecutiveFail is bumped and a candidate
// degrades at the 2-failure threshold (the scheduler does not demote in
// sticky mode). No-ops for unknown ids; sid="" just skips the pin drop.
func (p *credPool) signalEntryFailed(sid, entryID string) {
	p.mu.Lock()
	if sid != "" {
		delete(p.sessions, sid)
	}
	e, ok := p.entries[entryID]
	if !ok {
		p.mu.Unlock()
		return
	}
	e.consecutiveFail++
	if e.status == statusCandidate && e.consecutiveFail >= 2 {
		e.status = statusDegraded
	}
	count := e.consecutiveFail
	name := e.state.credName()
	p.mu.Unlock()
	fmt.Fprintf(errLog(), "ccm share: hard failure on %s(%s), unpinning session (failure %d/2)\n",
		name, shortID(entryID), count)
}

// noteSuccess records a successful (2xx) response for a session: it
// stamps the pin's lastSuccess (resetting the cache-grace clock), resets
// the entry's failure counter, and — for local entries (info != nil) —
// refreshes cached usage and feasibility. No-ops for unknown sid/entryID.
func (p *credPool) noteSuccess(sid, entryID string, info *oauth.UsageInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pin, ok := p.sessions[sid]; ok {
		pin.lastSuccess = p.now()
	}
	e, ok := p.entries[entryID]
	if !ok {
		// coverage: unreachable in production — entryID always names a
		// live entry threaded from handleServe. Defensive guard.
		return
	}
	e.consecutiveFail = 0
	if info != nil {
		e.lastUsage = info
		e.lastUsageAt = time.Now()
		e.lastFeasibility = computeFeasibility(info, p.now())
	}
}

// noteEntryUsage refreshes one entry's cached usage AND feasibility from a
// response's rate-limit headers WITHOUT touching its failure counter. Used
// on a 429: an exhausted window drops feasibility to 0 immediately so the
// next selection (after the pin is dropped) moves off the entry without
// waiting for a scheduler tick. No-op for nil info or unknown id.
func (p *credPool) noteEntryUsage(entryID string, info *oauth.UsageInfo) {
	if info == nil {
		// coverage: unreachable — caller gates on info != nil. Defensive.
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[entryID]
	if !ok {
		// coverage: unreachable in production — entryID names a live entry.
		return
	}
	e.lastUsage = info
	e.lastUsageAt = time.Now()
	e.lastFeasibility = computeFeasibility(info, p.now())
}

// dropPin removes a session's pin so its next request re-selects. Used on
// a quota-exhausted 429. No-op for absent sid.
func (p *credPool) dropPin(sid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, sid)
}

// quotaExhausted reports whether a parsed rate-limit snapshot indicates a
// usage-cap hit — any window at/above the exhaustion utilization. Used to
// classify a 429 as quota-exhausted (unpin) vs transient rate-limit (keep
// pin). nil info → false.
func quotaExhausted(info *oauth.UsageInfo) bool {
	if info == nil {
		return false
	}
	for _, q := range info.Quotas {
		if q.Used >= stickyExhaustionUtilization {
			return true
		}
	}
	return false
}

// evictSessions drops pins that are idle (no request within
// stickyIdleTTL) or stale (no successful response within
// stickyCacheGraceTTL — the prompt cache has expired, so there is nothing
// left to preserve). Called once per scheduler tick with the scheduler's
// clock time.
func (p *credPool) evictSessions(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for sid, pin := range p.sessions {
		idle := now.Sub(pin.lastSeen) >= stickyIdleTTL
		stale := now.Sub(pin.lastSuccess) >= stickyCacheGraceTTL
		if idle || stale {
			delete(p.sessions, sid)
		}
	}
}

// refreshFeasibilities recomputes lastFeasibility for every non-degraded
// entry from cached usage (or override). Sticky-mode selection reads
// lastFeasibility, so the scheduler keeps it fresh each tick without
// performing rotation.
func (p *credPool) refreshFeasibilities(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.status == statusDegraded {
			continue
		}
		switch {
		case e.feasibilityOverride != nil:
			e.lastFeasibility = *e.feasibilityOverride
		case e.lastUsage != nil:
			e.lastFeasibility = computeFeasibility(e.lastUsage, now)
		}
	}
}

// routeSession resolves the credential a session should use. On first
// contact it selects the best candidate and pins (sessionID -> entryID);
// it reuses the pin while the entry is healthy; on a gone/degraded entry
// it drops the pin and re-selects. An invalid/empty sessionID is routed
// to the best candidate WITHOUT storing a pin. Returns errNoActivated
// when no entry is eligible.
//
// Lock discipline: logMsg is set under the lock, but emitted AFTER the
// unlock. Defers run LIFO: p.mu.Unlock() (declared last) runs first,
// then the logging defer runs — so we never write to stderr while
// holding p.mu. This matches the pattern used by signalEntryFailed and
// SignalActivatedFailed throughout this package.
func (p *credPool) routeSession(sid string) (activatedView, string, tokenSource, error) {
	p.mu.Lock()
	var logMsg string
	// defers run LIFO: the Unlock (declared last) runs first, then this
	// logging defer runs AFTER the lock is released — never write to
	// stderr while holding p.mu.
	defer func() {
		if logMsg != "" {
			fmt.Fprint(errLog(), logMsg)
		}
	}()
	defer p.mu.Unlock()

	valid := usage.IsValidSessionID(sid)
	rePin := false
	oldName := ""
	if valid {
		if pin, ok := p.sessions[sid]; ok {
			if e, ok := p.entries[pin.entryID]; ok &&
				e.status != statusDegraded && e.consecutiveFail < 2 {
				pin.lastSeen = p.now()
				return p.viewForEntryLocked(pin.entryID) // steady-state reuse — NO log
			}
			// Entry gone or degraded — capture old name, drop pin, re-select.
			rePin = true
			oldName = shortID(pin.entryID)
			if e, ok := p.entries[pin.entryID]; ok {
				oldName = e.state.credName()
			}
			delete(p.sessions, sid)
		}
	}

	id, ok := p.bestCandidateLocked()
	if !ok {
		return activatedView{}, "", nil, errNoActivated
	}
	if valid {
		now := p.now()
		p.sessions[sid] = &sessionPin{entryID: id, lastSeen: now, lastSuccess: now}
		name := p.entries[id].state.credName()
		if rePin {
			logMsg = fmt.Sprintf("ccm share: sticky re-pin %s -> %s(%s) (was %s)\n",
				shortID(sid), name, shortID(id), oldName)
		} else {
			logMsg = fmt.Sprintf("ccm share: sticky pin %s -> %s(%s)\n",
				shortID(sid), name, shortID(id))
		}
	}
	return p.viewForEntryLocked(id)
}
