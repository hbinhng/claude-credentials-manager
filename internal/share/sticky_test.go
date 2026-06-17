package share

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
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

func TestSignalEntryFailedBumpsDegradesAndDropsPin(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{})
	eB.lastFeasibility = 50
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})
	_, _, _, _ = p.routeSession(sidA) // pins a

	p.signalEntryFailed(sidA, "a")
	if p.entries["a"].consecutiveFail != 1 {
		t.Errorf("fail = %d, want 1", p.entries["a"].consecutiveFail)
	}
	if p.entries["a"].status != statusCandidate {
		t.Errorf("status after 1 fail = %v, want candidate", p.entries["a"].status)
	}
	if _, ok := p.sessions[sidA]; ok {
		t.Error("pin should be dropped on hard failure")
	}

	_, _, _, _ = p.routeSession(sidA) // re-pins a (still best, fail 1<2)
	p.signalEntryFailed(sidA, "a")
	if p.entries["a"].status != statusDegraded {
		t.Errorf("status after 2 fails = %v, want degraded", p.entries["a"].status)
	}
}

func TestSignalEntryFailedUnknownEntryNoPanic(t *testing.T) {
	p, _ := stickyPool(t, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusCandidate, &fakeTokenSource{}),
	})
	p.signalEntryFailed("", "ghost") // must not panic
}

func TestQuotaExhausted(t *testing.T) {
	if quotaExhausted(nil) {
		t.Error("nil info must not be exhausted")
	}
	low := &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 40}, {Name: "7d", Used: 80}}}
	if quotaExhausted(low) {
		t.Error("all windows below threshold must not be exhausted")
	}
	hi := &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 40}, {Name: "7d", Used: 100}}}
	if !quotaExhausted(hi) {
		t.Error("a window at 100%% must be exhausted")
	}
}

func TestNoteSuccessStampsLastSuccessAndResetsFail(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.consecutiveFail = 1
	p, clk := stickyPool(t, map[string]*poolEntry{"a": eA})
	_, _, _, _ = p.routeSession(sidA) // pin created; lastSuccess = t0
	clk.Advance(5 * time.Minute)

	info := &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10, ResetsAt: "2099-01-01T00:00:00Z"}}}
	p.noteSuccess(sidA, "a", info)

	if p.entries["a"].consecutiveFail != 0 {
		t.Errorf("consecutiveFail = %d, want 0", p.entries["a"].consecutiveFail)
	}
	if !p.sessions[sidA].lastSuccess.Equal(clk.Now()) {
		t.Errorf("lastSuccess = %v, want %v", p.sessions[sidA].lastSuccess, clk.Now())
	}
	if p.entries["a"].lastUsage != info {
		t.Error("lastUsage not stored")
	}
}

func TestNoteEntryUsageRefreshesFeasibilityWithoutTouchingFail(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.consecutiveFail = 1
	eA.lastFeasibility = 999
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA}) // clock = Unix(1_700_000_000)

	// A fully-utilized 5h window resetting within the window → feasibility 0.
	reset := time.Unix(1_700_000_000+3600, 0).UTC().Format(time.RFC3339)
	info := &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 100, ResetsAt: reset}}}
	p.noteEntryUsage("a", info)

	if p.entries["a"].consecutiveFail != 1 {
		t.Errorf("consecutiveFail = %d, want 1 (unchanged)", p.entries["a"].consecutiveFail)
	}
	if p.entries["a"].lastFeasibility != 0 {
		t.Errorf("lastFeasibility = %v, want 0 (exhausted window)", p.entries["a"].lastFeasibility)
	}
}

func TestEvictSessionsDropsIdleAndStaleKeepsFresh(t *testing.T) {
	const sidC = "33333333-3333-3333-3333-333333333333"
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.lastFeasibility = 100
	p, clk := stickyPool(t, map[string]*poolEntry{"a": eA})

	// Three sessions pinned at t0 (lastSeen = lastSuccess = t0).
	_, _, _, _ = p.routeSession(sidA)
	_, _, _, _ = p.routeSession(sidB)
	_, _, _, _ = p.routeSession(sidC)

	// +45m: sidA fully refreshed (request + success); sidB request only
	// (no success); sidC untouched.
	clk.Advance(45 * time.Minute)
	_, _, _, _ = p.routeSession(sidA)
	p.noteSuccess(sidA, "a", nil)
	_, _, _, _ = p.routeSession(sidB)

	p.evictSessions(clk.Now())
	if _, ok := p.sessions[sidC]; ok {
		t.Error("sidC idle 45m (>= 30m TTL) should be evicted")
	}
	if _, ok := p.sessions[sidB]; !ok {
		t.Error("sidB seen recently should be kept (45m < 60m grace)")
	}
	if _, ok := p.sessions[sidA]; !ok {
		t.Error("fresh sidA should be kept")
	}

	// +20m more (65m since sidB's last success): keep sidB's lastSeen fresh
	// so idle does not fire, and confirm the grace sweep drops it.
	clk.Advance(20 * time.Minute)
	_, _, _, _ = p.routeSession(sidB)
	p.evictSessions(clk.Now())
	if _, ok := p.sessions[sidB]; ok {
		t.Error("sidB with no success for >60m should be evicted by grace")
	}
	if _, ok := p.sessions[sidA]; !ok {
		t.Error("sidA should survive the second sweep (within both TTLs)")
	}
}

func TestRefreshFeasibilities(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.lastUsage = &oauth.UsageInfo{Quotas: []oauth.Quota{
		{Name: "5h", Used: 50, ResetsAt: "2026-01-01T01:00:00Z"},
	}}
	override := 4242.0
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{})
	eB.feasibilityOverride = &override
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})

	p.refreshFeasibilities(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if p.entries["a"].lastFeasibility <= 0 {
		t.Errorf("entry a feasibility not recomputed: %v", p.entries["a"].lastFeasibility)
	}
	if p.entries["b"].lastFeasibility != 4242.0 {
		t.Errorf("entry b feasibility = %v, want override 4242", p.entries["b"].lastFeasibility)
	}
}

func TestSnapshotLinesIncludesStickyPinCount(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.lastFeasibility = 100
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA}) // enableSticky sets p.sticky
	_, _, _, _ = p.routeSession(sidA)

	joined := strings.Join(p.SnapshotLines(), "\n")
	if !strings.Contains(joined, "sticky: 1 active session pin") {
		t.Errorf("snapshot missing sticky pin count line:\n%s", joined)
	}
}

func TestStartSessionStickyPreflightFailsWhenNoCandidate(t *testing.T) {
	ResetLastSchedulerForTest()
	t.Cleanup(ResetLastSchedulerForTest)

	now := time.Unix(1_700_000_000, 0)
	clk := newFakeClock(now)

	eA := newEntry("a", "alice", statusDegraded, &fakeTokenSource{token: "tokA"})
	eA.lastUsage = &oauth.UsageInfo{}
	eA.lastUsageAt = now
	pool := makePool("", false, map[string]*poolEntry{"a": eA})

	sess, err := StartSession(nil, Options{
		BindHost:          "127.0.0.1",
		Pool:              pool,
		RebalanceInterval: time.Minute,
		Clock:             clk,
		Sticky:            true,
		InitialEntryID:    "a",
		InitialEntryName:  "alice",
	})
	if err == nil {
		if sess != nil {
			_ = sess.Stop()
		}
		t.Fatal("expected StartSession to fail when no candidate is eligible")
	}
	if !strings.Contains(err.Error(), "no usable credential") {
		t.Errorf("error = %q, want it to mention 'no usable credential'", err.Error())
	}
}

func TestStartSessionEnablesStickyOnPool(t *testing.T) {
	ResetLastSchedulerForTest()
	t.Cleanup(ResetLastSchedulerForTest)

	// Seed fresh cache (lastUsage non-nil + lastUsageAt == clock now) so
	// the sticky preflight tick is a cache hit and never calls the real
	// oauth.FetchUsageFn (no network). Empty UsageInfo → +Inf feasibility,
	// so bestCandidate succeeds and the preflight passes.
	now := time.Unix(1_700_000_000, 0)
	clk := newFakeClock(now)

	eA := newEntry("a", "alice", statusActivated, &fakeTokenSource{token: "tokA"})
	eA.captured = http.Header{"User-Agent": {"ccm"}}
	eA.lastUsage = &oauth.UsageInfo{}
	eA.lastUsageAt = now
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastUsage = &oauth.UsageInfo{}
	eB.lastUsageAt = now
	eB.lastFeasibility = 50
	pool := makePool("a", false, map[string]*poolEntry{"a": eA, "b": eB})

	// Bind LAN so no Cloudflare tunnel is started.
	sess, err := StartSession(nil, Options{
		BindHost:          "127.0.0.1",
		Pool:              pool,
		RebalanceInterval: time.Minute,
		Clock:             clk,
		Sticky:            true,
		InitialEntryID:    "a",
		InitialEntryName:  "alice",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Stop() })

	if !pool.sticky {
		t.Error("pool.sticky not enabled by StartSession")
	}
	if pool.activated != "" {
		t.Errorf("activated = %q, want empty in sticky mode", pool.activated)
	}
}

func TestRouteSessionLogsPinAndRePin(t *testing.T) {
	// Swap errLog to capture output.
	orig := errLog
	var buf bytes.Buffer
	errLog = func() io.Writer { return &buf }
	defer func() { errLog = orig }()

	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{token: "tokA"})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastFeasibility = 50
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA, "b": eB})

	// First routeSession(sidA) -> new pin to alice.
	_, _, _, err := p.routeSession(sidA)
	if err != nil {
		t.Fatalf("routeSession err: %v", err)
	}
	logAfterPin := buf.String()
	if !strings.Contains(logAfterPin, "sticky pin") {
		t.Errorf("first pin must log 'sticky pin'; got: %q", logAfterPin)
	}
	if !strings.Contains(logAfterPin, shortID(sidA)) {
		t.Errorf("log must contain sid prefix; got: %q", logAfterPin)
	}
	if !strings.Contains(logAfterPin, "alice") {
		t.Errorf("log must contain cred name 'alice'; got: %q", logAfterPin)
	}

	// Second routeSession(sidA) -> steady-state reuse; must add NO new log.
	lenBefore := buf.Len()
	_, _, _, _ = p.routeSession(sidA)
	if buf.Len() != lenBefore {
		t.Errorf("steady-state reuse must not log; got new bytes: %q", buf.String()[lenBefore:])
	}

	// Degrade entry a; next routeSession(sidA) -> re-pin to bob.
	p.entries["a"].status = statusDegraded
	_, id, _, err := p.routeSession(sidA)
	if err != nil {
		t.Fatalf("re-pin routeSession err: %v", err)
	}
	if id != "b" {
		t.Errorf("re-pin should route to bob; got %q", id)
	}
	logAfterRePin := buf.String()
	if !strings.Contains(logAfterRePin, "sticky re-pin") {
		t.Errorf("re-pin must log 'sticky re-pin'; got: %q", logAfterRePin)
	}
	if !strings.Contains(logAfterRePin, "(was alice)") {
		t.Errorf("re-pin log must contain '(was alice)'; got: %q", logAfterRePin)
	}
	if !strings.Contains(logAfterRePin, "bob") {
		t.Errorf("re-pin log must contain new cred name 'bob'; got: %q", logAfterRePin)
	}

	// Invalid sid must log nothing additional.
	lenBeforeInvalid := buf.Len()
	_, _, _, _ = p.routeSession("not-a-uuid")
	if buf.Len() != lenBeforeInvalid {
		t.Errorf("invalid sid must not log; got new bytes: %q", buf.String()[lenBeforeInvalid:])
	}
}

func TestSnapshotLinesListsStickyPins(t *testing.T) {
	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{})
	eA.lastFeasibility = 100
	p, _ := stickyPool(t, map[string]*poolEntry{"a": eA})

	_, _, _, _ = p.routeSession(sidA)

	joined := strings.Join(p.SnapshotLines(), "\n")
	if !strings.Contains(joined, shortID(sidA)) {
		t.Errorf("snapshot must list sidA prefix %q; got:\n%s", shortID(sidA), joined)
	}
	if !strings.Contains(joined, "alice") {
		t.Errorf("snapshot must list cred name 'alice'; got:\n%s", joined)
	}
}
