package share

import (
	"encoding/json"
	"net/http"
)

// handleUsage serves GET /ccm-share/usage. Bearer auth via
// p.accessToken (same as /v1/messages). Emits the minimal JSON
// shape defined in §3 of the share-passthrough spec.
func (p *Proxy) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+p.accessToken {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing access token")
		return
	}

	body := usageResponseBody{V: 1}

	if p.pool == nil {
		// Single-cred mode: no scheduling pressure.
		body.Activated = true
		body.Degraded = false
		body.Unconstrained = true
		body.FeasibilitySeconds = nil
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
		return
	}

	view := p.pool.snapshotForUsage()
	if !view.activated {
		body.Activated = false
		body.Degraded = view.anyDegraded
		body.Unconstrained = false
		zero := 0.0
		body.FeasibilitySeconds = &zero
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(body)
		return
	}

	body.Activated = true
	body.Degraded = view.anyDegraded
	body.Unconstrained = view.unconstrained
	if !view.unconstrained {
		f := view.feasibility
		body.FeasibilitySeconds = &f
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// usageView is the read-only snapshot used by handleUsage. Kept
// separate from activatedView so the two concerns don't drift.
type usageView struct {
	activated     bool
	feasibility   float64
	unconstrained bool
	anyDegraded   bool
}

// snapshotForUsage returns the usageView under p.mu.RLock.
func (p *credPool) snapshotForUsage() usageView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v := usageView{}
	for _, e := range p.entries {
		if e.status == statusDegraded {
			v.anyDegraded = true
		}
	}
	if p.activated == "" {
		return v
	}
	e, ok := p.entries[p.activated]
	if !ok {
		// coverage: unreachable — pool invariant.
		return v
	}
	v.activated = true
	// Saturating override (passthrough whose upstream was unconstrained)
	// reports as unconstrained. Detection: feasibilityOverride == nil
	// would NOT mean unconstrained — only MaxFloat64 override does.
	if e.feasibilityOverride != nil && *e.feasibilityOverride >= 1e308 {
		v.unconstrained = true
		return v
	}
	v.feasibility = e.lastFeasibility
	return v
}
