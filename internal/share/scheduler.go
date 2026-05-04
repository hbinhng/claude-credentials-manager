// note: deviation from plan — runOnce snapshots consecutiveFail per entry
// before probes run (preFail map). Without this, MarkProbe(success) resets
// the counter and the SignalActivatedFailed signal is lost in the same
// tick, so TestLoadBalanceUpstream401FastPath fails. Eligibility/demotion
// uses max(preFail, current) so the 401 signal survives a tick where the
// probe also succeeded.
package share

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// computeFeasibility returns the rotation score for a single
// credential's usage snapshot. See the design doc for the formula
// and edge-case rules (clamps + missing-window fallbacks).
func computeFeasibility(info *oauth.UsageInfo, now time.Time) float64 {
	left5h, wait5h := windowInputs(lookupQuota(info.Quotas, "5h"), now)
	left7d, wait7d := windowInputs(lookupQuota(info.Quotas, "7d"), now)
	return left5h/wait5h + 0.7*left7d/wait7d
}

// windowInputs returns (left%, wait_seconds) for a single quota
// window, applying clamps and best-case fallbacks for nil/missing.
func windowInputs(q *oauth.Quota, now time.Time) (float64, float64) {
	if q == nil {
		return 100, 1
	}
	left := math.Max(0, math.Min(100, 100-q.Used))
	wait := math.Max(1, secondsUntil(q.ResetsAt, now))
	return left, wait
}

func secondsUntil(stamp string, now time.Time) float64 {
	if stamp == "" {
		return 1
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return 1
		}
	}
	return t.Sub(now).Seconds()
}

func lookupQuota(qs []oauth.Quota, name string) *oauth.Quota {
	for i := range qs {
		if qs[i].Name == name {
			return &qs[i]
		}
	}
	return nil
}

// usageProbe is the test seam for the per-entry usage HTTP call.
// In production it composes credState.Fresh + oauth.FetchUsageFn;
// in tests it is a stub.
type usageProbe func(state poolEntryState) (*oauth.UsageInfo, error)

// productionProbe is the production wiring of usageProbe: refresh
// the credential if needed, then call FetchUsageFn.
func productionProbe(state poolEntryState) (*oauth.UsageInfo, error) {
	tok, err := state.Fresh()
	if err != nil {
		return nil, fmt.Errorf("usage probe refresh: %w", err)
	}
	info := oauth.FetchUsageFn(tok)
	if info == nil {
		// coverage: unreachable — FetchUsage always returns a
		// non-nil pointer; defensive guard.
		return nil, fmt.Errorf("usage probe: nil response")
	}
	if info.Error != "" {
		return nil, fmt.Errorf("usage probe: %s", info.Error)
	}
	return info, nil
}

type scheduler struct {
	pool     *credPool
	probe    usageProbe
	clock    clock
	interval time.Duration
	debug    bool
}

func newScheduler(pool *credPool, probe usageProbe, c clock, interval time.Duration) *scheduler {
	return &scheduler{pool: pool, probe: probe, clock: c, interval: interval}
}

// SetDebug enables verbose tick-keep logs.
func (s *scheduler) SetDebug(b bool) { s.debug = b }

// Run blocks until done is closed, firing runOnce on every tick.
func (s *scheduler) Run(done <-chan struct{}) {
	tick := s.clock.NewTicker(s.interval)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C():
			s.runOnce()
		}
	}
}

// runOnce performs one tick: probe every entry, recompute
// feasibility, and apply the rotation rule.
func (s *scheduler) runOnce() {
	type job struct {
		id    string
		state poolEntryState
	}
	s.pool.mu.RLock()
	jobs := make([]job, 0, len(s.pool.entries))
	// preFail snapshots each entry's consecutiveFail before probes
	// run, so an upstream-401 signal (SignalActivatedFailed) is not
	// reset by a subsequent probe-success in the same tick.
	preFail := make(map[string]int, len(s.pool.entries))
	for id, e := range s.pool.entries {
		jobs = append(jobs, job{id: id, state: e.state})
		preFail[id] = e.consecutiveFail
	}
	s.pool.mu.RUnlock()

	for _, j := range jobs {
		info, err := s.probe(j.state)
		if err != nil {
			fmt.Fprintf(errLog(), "ccm share: probe failed for %s: %v\n", shortID(j.id), err)
		}
		s.pool.MarkProbe(j.id, info, err)
	}

	// Compute feasibility for eligible entries; pick winner.
	now := s.clock.Now()
	type cand struct {
		id          string
		feasibility float64
	}
	s.pool.mu.Lock()
	// NOTE: no `defer Unlock()` — we release explicitly below so
	// stderr writes don't block request-path Fresh() readers.

	var eligible []cand
	for id, e := range s.pool.entries {
		eligibleEntry := false
		switch e.status {
		case statusCandidate:
			eligibleEntry = true
		case statusActivated:
			// Use max(pre-tick fail count, current fail count). The
			// pre-tick value preserves any upstream-401 signal that
			// the probe path may have reset on success; the current
			// value picks up new probe failures in this tick.
			fail := e.consecutiveFail
			if pf, ok := preFail[id]; ok && pf > fail {
				fail = pf
			}
			eligibleEntry = fail < 2
		}
		if !eligibleEntry || e.lastUsage == nil {
			continue
		}
		f := computeFeasibility(e.lastUsage, now)
		e.lastFeasibility = f
		eligible = append(eligible, cand{id: id, feasibility: f})
	}

	// Sort: highest feasibility first, ties by ID lex ascending.
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].feasibility != eligible[j].feasibility {
			return eligible[i].feasibility > eligible[j].feasibility
		}
		return eligible[i].id < eligible[j].id
	})

	// Decide what changes to make under the lock; defer logging
	// until AFTER we release the lock so a slow stderr write does
	// not block request-path Fresh() readers.
	type logEntry struct {
		kind                           string // "rotate" | "demote" | "keep"
		oldName, newName               string
		oldID, newID                   string
		oldFeasibility, newFeasibility float64
	}
	var pending logEntry

	if len(eligible) == 0 {
		// No eligible winner.
		actEntry, hasAct := s.pool.entries[s.pool.activated]
		actFail := 0
		if hasAct {
			actFail = actEntry.consecutiveFail
			// coverage: unreachable in tests — when no entry is
			// eligible, every probe must have failed for this branch
			// to matter, so preFail (pre-tick) and current
			// consecutiveFail are both >= 2. preFail > current can
			// only happen on a probe-success that reset current to 0,
			// but that would have made activated eligible. Kept for
			// defensive symmetry with the rotation path.
			if pf, ok := preFail[s.pool.activated]; ok && pf > actFail {
				actFail = pf
			}
		}
		if hasAct && actFail >= 2 && !s.pool.singleton {
			// Branch (c): demote activated.
			pending = logEntry{kind: "demote", oldName: actEntry.state.credName(), oldID: s.pool.activated}
			actEntry.status = statusDegraded
			s.pool.activated = ""
		} else if s.debug {
			// Branch (d) debug log only.
			if hasAct {
				pending = logEntry{kind: "keep", oldName: actEntry.state.credName(), oldID: s.pool.activated}
			}
		}
	} else {
		winner := eligible[0]
		if winner.id != s.pool.activated {
			// Branch (b): rotation.
			oldID := s.pool.activated
			oldEntry, hasOld := s.pool.entries[oldID]
			newEntry := s.pool.entries[winner.id]
			if hasOld {
				oldFail := oldEntry.consecutiveFail
				if pf, ok := preFail[oldID]; ok && pf > oldFail {
					oldFail = pf
				}
				if oldFail >= 2 {
					oldEntry.status = statusDegraded
				} else {
					oldEntry.status = statusCandidate
					oldEntry.consecutiveFail = 0
				}
			}
			newEntry.status = statusActivated
			newEntry.consecutiveFail = 0
			s.pool.activated = winner.id

			oldName := "(none)"
			oldFeas := 0.0
			if hasOld {
				oldName = oldEntry.state.credName()
				oldFeas = oldEntry.lastFeasibility
			}
			pending = logEntry{
				kind: "rotate", oldName: oldName, newName: newEntry.state.credName(),
				oldID: oldID, newID: winner.id,
				oldFeasibility: oldFeas, newFeasibility: winner.feasibility,
			}
		}
	}

	s.pool.mu.Unlock()
	// Lock released before stderr write.
	switch pending.kind {
	case "rotate":
		fmt.Fprintf(errLog(), "ccm share: rotated activated %s(%s) → %s(%s) (feasibility %.3f → %.3f)\n",
			pending.oldName, shortID(pending.oldID), pending.newName, shortID(pending.newID),
			pending.oldFeasibility, pending.newFeasibility)
	case "demote":
		fmt.Fprintf(errLog(), "ccm share: %s(%s) degraded; no usable credentials, serving 503 until recovery\n",
			pending.oldName, shortID(pending.oldID))
	case "keep":
		fmt.Fprintf(errLog(), "ccm share [debug]: no eligible candidate, keeping activated %s(%s)\n",
			pending.oldName, shortID(pending.oldID))
	}
}
