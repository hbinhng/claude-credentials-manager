package share

import (
	"net/http"
	"testing"
	"time"
)

// stickyPool builds a pool and switches it into sticky mode with a fake
// clock. enableSticky initializes the sessions map (so routeSession can
// store pins) and records the clock for pin timestamps / eviction.
func stickyPool(t *testing.T, entries map[string]*poolEntry) (*credPool, *fakeClock) {
	t.Helper()
	p := makePool("", false, entries)
	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	p.enableSticky(clk)
	return p, clk
}

func TestEnableStickyResetsActivatedAndStoresCapture(t *testing.T) {
	eA := newEntry("a", "alice", statusActivated, &fakeTokenSource{})
	eA.captured = http.Header{"User-Agent": {"ccm-test"}}
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{})
	p := makePool("a", false, map[string]*poolEntry{"a": eA, "b": eB})

	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	p.enableSticky(clk)

	if !p.sticky {
		t.Error("sticky flag not set")
	}
	if p.activated != "" {
		t.Errorf("activated = %q, want empty in sticky mode", p.activated)
	}
	if p.entries["a"].status != statusCandidate {
		t.Errorf("activated entry not reset to candidate: %v", p.entries["a"].status)
	}
	if p.localCaptured.Get("User-Agent") != "ccm-test" {
		t.Errorf("localCaptured not derived from captured entry: %v", p.localCaptured)
	}
	if p.sessions == nil {
		t.Error("sessions map not initialized")
	}
}

func TestBestCandidatePicksHighestFeasibility(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.lastFeasibility = 50
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{})
	eB.lastFeasibility = 100
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})

	id, ok := p.bestCandidate()
	if !ok || id != "b" {
		t.Errorf("bestCandidate = (%q,%v), want (b,true)", id, ok)
	}
}

func TestBestCandidateTieBreakByID(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{})
	eB.lastFeasibility = 100
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})

	id, _ := p.bestCandidate()
	if id != "a" {
		t.Errorf("tie should break to lex-smallest id; got %q", id)
	}
}

func TestBestCandidateSkipsDegradedAndFailed(t *testing.T) {
	eA := newEntry("a", "alice", statusDegraded, &fakeTokenSource{})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{})
	eB.lastFeasibility = 10
	eB.consecutiveFail = 2 // not eligible
	eC := newEntry("c", "carol", statusCandidate, &fakeTokenSource{})
	eC.lastFeasibility = 5
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB, "c": eC})

	id, ok := p.bestCandidate()
	if !ok || id != "c" {
		t.Errorf("bestCandidate = (%q,%v), want (c,true)", id, ok)
	}
}

func TestBestCandidateNoneEligible(t *testing.T) {
	eA := newEntry("a", "alice", statusDegraded, &fakeTokenSource{})
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA})

	if _, ok := p.bestCandidate(); ok {
		t.Error("bestCandidate should report ok=false when all degraded")
	}
}

const (
	sidA = "11111111-1111-1111-1111-111111111111"
	sidB = "22222222-2222-2222-2222-222222222222"
)

func TestRouteSessionPinsOnFirstContact(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{token: "tokA"})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastFeasibility = 50
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})

	_, id, ts, err := p.routeSession(sidA)
	if err != nil {
		t.Fatalf("routeSession err: %v", err)
	}
	if id != "a" {
		t.Errorf("first contact should pick best (a); got %q", id)
	}
	if tok, _ := ts.Fresh(); tok != "tokA" {
		t.Errorf("token source = %q, want tokA", tok)
	}
	if p.sessions[sidA] == nil || p.sessions[sidA].entryID != "a" {
		t.Errorf("pin not stored for sidA: %+v", p.sessions[sidA])
	}
}

func TestRouteSessionStaysPinnedAcrossFeasibilityShift(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{token: "tokA"})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastFeasibility = 50
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})

	// S1 pins to a (best).
	_, id1, _, _ := p.routeSession(sidA)
	if id1 != "a" {
		t.Fatalf("setup: want a, got %q", id1)
	}
	// Feasibility shifts: b is now better than a.
	p.entries["a"].lastFeasibility = 10
	p.entries["b"].lastFeasibility = 200

	// S1 STAYS on a (cache continuity).
	_, id1again, _, _ := p.routeSession(sidA)
	if id1again != "a" {
		t.Errorf("pinned session moved on feasibility shift: got %q, want a", id1again)
	}
	// A NEW session picks the now-best b.
	_, id2, _, _ := p.routeSession(sidB)
	if id2 != "b" {
		t.Errorf("new session should pick current best b; got %q", id2)
	}
}

func TestRouteSessionRepinWhenPinnedEntryDegrades(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{token: "tokA"})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastFeasibility = 50
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})

	_, _, _, _ = p.routeSession(sidA) // pins a
	p.entries["a"].status = statusDegraded

	_, id, _, _ := p.routeSession(sidA)
	if id != "b" {
		t.Errorf("session should re-pin to b after a degrades; got %q", id)
	}
	if p.sessions[sidA].entryID != "b" {
		t.Errorf("pin not updated to b: %+v", p.sessions[sidA])
	}
}

func TestRouteSessionInvalidSessionIDNotPinned(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{token: "tokA"})
	eA.lastFeasibility = 100
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA})

	_, id, _, err := p.routeSession("not-a-uuid")
	if err != nil {
		t.Fatalf("routeSession err: %v", err)
	}
	if id != "a" {
		t.Errorf("invalid sid should still route to best; got %q", id)
	}
	if len(p.sessions) != 0 {
		t.Errorf("invalid sid must not create a pin; sessions=%v", p.sessions)
	}
}

func TestRouteSessionNoEligibleReturnsErrNoActivated(t *testing.T) {
	eA := newEntry("a", "alice", statusDegraded, &fakeTokenSource{})
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA})

	_, _, _, err := p.routeSession(sidA)
	if err != errNoActivated {
		t.Errorf("err = %v, want errNoActivated", err)
	}
}

func TestRouteSessionPassthroughView(t *testing.T) {
	pt := newPassthroughEntryState(Ticket{Scheme: "https", Host: "u.example", Token: "ptok"})
	e := &poolEntry{state: pt, status: statusCandidate, lastFeasibility: 100}
	p, _ := stickyPool(t, map[string]*poolEntry{pt.credID(): e})
	p.localCaptured = http.Header{"User-Agent": {"x"}}

	view, _, ts, err := p.routeSession(sidA)
	if err != nil {
		t.Fatalf("routeSession err: %v", err)
	}
	if !view.isPassthrough {
		t.Error("view.isPassthrough = false, want true")
	}
	if view.captured != nil {
		t.Errorf("passthrough view must not carry captured headers; got %v", view.captured)
	}
	if tok, _ := ts.Fresh(); tok != "ptok" {
		t.Errorf("passthrough token = %q, want ptok", tok)
	}
}
