package share

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func ptrFloat(f float64) *float64 { return &f }

// stubCaptureCredOKScheduler installs a default captureCredFn that
// returns canned headers for any cred. Used by scheduler tests
// where rotation triggers capture but the test doesn't care about
// header content.
func stubCaptureCredOKScheduler(t *testing.T) {
	t.Helper()
	orig := captureCredFn
	captureCredFn = func(_ *store.Credential, _ string) (http.Header, error) {
		return http.Header{"User-Agent": []string{"stub"}}, nil
	}
	t.Cleanup(func() { captureCredFn = orig })
}

func TestFormatLifetime(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0s"},
		{1, "1s"},
		{60, "1m0s"},
		{3600, "1h0m0s"},
		{9000, "2h30m0s"},
		{604800, "168h0m0s"},
		{math.Inf(1), "∞"},
	}
	for _, tt := range tests {
		got := formatLifetime(tt.in)
		if got != tt.want {
			t.Errorf("formatLifetime(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestComputeFeasibilityBothPresent(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 20, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// left5h = 50, wait5h = 3600, left7d = 80, wait7d = 86400
	// = 50/3600 + 0.7 * 80/86400
	want := 50.0/3600 + 0.7*80.0/86400
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibility5hMissing(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "7d", Used: 20, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// 5h falls back: left=100, wait=1; 7d normal
	want := 100.0/1.0 + 0.7*80.0/86400
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibility7dMissing(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	want := 50.0/3600 + 0.7*100.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityBothMissing(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{Quotas: nil}
	got := computeFeasibility(info, now)
	want := 100.0/1.0 + 0.7*100.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityUsedOver100(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 150, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 0, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// left5h clamps to 0
	want := 0.0/3600 + 0.7*100.0/86400
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityResetInPast(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: now.Add(-time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 20, ResetsAt: now.Add(-time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	want := 50.0/1.0 + 0.7*80.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityParseFailure(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: "not-a-timestamp"},
			{Name: "7d", Used: 20, ResetsAt: ""},
		},
	}
	got := computeFeasibility(info, now)
	want := 50.0/1.0 + 0.7*80.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityIgnoresModelExtras(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "7d/sonnet-4-5", Used: 10, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
			{Name: "7d/opus-4-7", Used: 5, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// Both unscoped windows missing → both fallbacks
	want := 100.0/1.0 + 0.7*100.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLookupQuota(t *testing.T) {
	qs := []oauth.Quota{
		{Name: "5h", Used: 1},
		{Name: "7d", Used: 2},
		{Name: "7d/sonnet", Used: 3},
	}
	if got := lookupQuota(qs, "5h"); got == nil || got.Used != 1 {
		t.Errorf("lookup 5h failed: %+v", got)
	}
	if got := lookupQuota(qs, "7d"); got == nil || got.Used != 2 {
		t.Errorf("lookup 7d failed: %+v", got)
	}
	if got := lookupQuota(qs, "missing"); got != nil {
		t.Errorf("lookup missing returned %+v, want nil", got)
	}
}

func TestSchedulerRotatesToHigherFeasibility(t *testing.T) {
	stubCaptureCredOKScheduler(t)
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}

	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated},
		"b": {state: stateB, status: statusCandidate},
	}, activated: "a"}

	probes := map[string]*oauth.UsageInfo{
		"a": {Quotas: []oauth.Quota{
			{Name: "5h", Used: 90, ResetsAt: now.Add(4 * time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 80, ResetsAt: now.Add(7 * 24 * time.Hour).Format(time.RFC3339)},
		}},
		"b": {Quotas: []oauth.Quota{
			{Name: "5h", Used: 10, ResetsAt: now.Add(30 * time.Minute).Format(time.RFC3339)},
			{Name: "7d", Used: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
		}},
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return probes[state.credID()], nil
	}

	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()

	if pool.activated != "b" {
		t.Errorf("activated = %q, want b (higher feasibility)", pool.activated)
	}
}

func TestSchedulerProbeFailureBumpsCounter(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}

	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated},
		"b": {state: stateB, status: statusCandidate},
	}, activated: "a"}

	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		if state.credID() == "b" {
			return nil, fmt.Errorf("boom")
		}
		return &oauth.UsageInfo{}, nil
	}

	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()
	if pool.entries["b"].status != statusCandidate {
		t.Errorf("status after 1 fail = %v, want candidate", pool.entries["b"].status)
	}
	sch.runOnce()
	if pool.entries["b"].status != statusDegraded {
		t.Errorf("status after 2 fails = %v, want degraded", pool.entries["b"].status)
	}
}

func TestSchedulerActivatedDemotesWhenAllElseDegraded(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}

	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated, consecutiveFail: 2},
		"b": {state: stateB, status: statusDegraded, consecutiveFail: 5},
	}, activated: "a"}

	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return nil, fmt.Errorf("everything is broken")
	}

	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()

	if pool.activated != "" {
		t.Errorf("activated = %q, want empty (Demote should have been called)", pool.activated)
	}
}

func TestSchedulerNonSingletonOneEntryFailingDemotes(t *testing.T) {
	// activated starts at consecutiveFail=1 (healthy). One failed
	// probe pushes it to 2. Pool is non-singleton (no second entry,
	// but `singleton: false` so the demote-to-503 path applies). The
	// scheduler MUST demote: branch (c).
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated, consecutiveFail: 1},
	}, activated: "a"}

	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return nil, fmt.Errorf("blip")
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()

	if pool.activated != "" {
		t.Errorf("activated = %q, want empty (Demote should fire on branch c)", pool.activated)
	}
	if pool.entries["a"].status != statusDegraded {
		t.Errorf("a status = %v, want degraded", pool.entries["a"].status)
	}
}

func TestSchedulerSingletonNeverDemotes(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return nil, fmt.Errorf("boom")
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	for i := 0; i < 5; i++ {
		sch.runOnce()
	}
	if pool.activated != "a" {
		t.Errorf("singleton pool activated changed to %q", pool.activated)
	}
}

func TestSchedulerTieBreakByIDLex(t *testing.T) {
	stubCaptureCredOKScheduler(t)
	now := time.Now()
	stateA := &fakeRefreshableState{id: "aaaa", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "bbbb", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"aaaa": {state: stateA, status: statusCandidate},
		"bbbb": {state: stateB, status: statusActivated},
	}, activated: "bbbb"}
	// Both probes identical → identical feasibility.
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return &oauth.UsageInfo{}, nil
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()
	if pool.activated != "aaaa" {
		t.Errorf("activated = %q, want aaaa (tie broken by lex)", pool.activated)
	}
}

func TestSchedulerDebugLogsWhenNoEligible(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated, consecutiveFail: 1}},
		activated: "a",
		singleton: true,
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return nil, fmt.Errorf("blip")
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.SetDebug(true)
	// Two ticks: first push counter to 2, second triggers no-eligible
	// path. Singleton means activated stays put → debug branch.
	sch.runOnce()
	sch.runOnce()
	// pool.activated should still be "a" (singleton can't demote).
	if pool.activated != "a" {
		t.Errorf("singleton activated changed to %q", pool.activated)
	}
}

func TestSchedulerPreFailRotationPath(t *testing.T) {
	stubCaptureCredOKScheduler(t)
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated, consecutiveFail: 3, lastUsage: &oauth.UsageInfo{}},
		"b": {state: stateB, status: statusCandidate, lastUsage: &oauth.UsageInfo{}},
	}, activated: "a"}
	// Probe success for both — would normally reset a.consecutiveFail.
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return &oauth.UsageInfo{}, nil
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()
	// preFail snapshot remembered consecutiveFail=3 → eligibility check
	// rotates winner b in. a's status should be degraded (preFail >= 2 path).
	if pool.activated != "b" {
		t.Errorf("activated = %q, want b (preFail rotation)", pool.activated)
	}
	if pool.entries["a"].status != statusDegraded {
		t.Errorf("a status = %v, want degraded (preFail rotation degrade path)", pool.entries["a"].status)
	}
}

func TestSchedulerDegradedEntryReprobedEachTick(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated},
		"b": {state: stateB, status: statusDegraded, consecutiveFail: 5},
	}, activated: "a"}

	probeCalls := map[string]int{}
	failBob := true
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		probeCalls[state.credID()]++
		if state.credID() == "b" && failBob {
			return nil, fmt.Errorf("still down")
		}
		return &oauth.UsageInfo{}, nil
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)

	sch.runOnce()
	if probeCalls["b"] != 1 {
		t.Errorf("tick 1: probeCalls[b] = %d, want 1", probeCalls["b"])
	}
	if pool.entries["b"].status != statusDegraded {
		t.Errorf("b should still be degraded")
	}

	failBob = false
	sch.runOnce()
	if probeCalls["b"] != 2 {
		t.Errorf("tick 2: probeCalls[b] = %d, want 2 (degraded entries probed every tick)", probeCalls["b"])
	}
	if pool.entries["b"].status != statusCandidate {
		t.Errorf("b should have recovered to candidate")
	}
}

func TestProductionProbeRefreshError(t *testing.T) {
	state := &fakeRefreshableState{id: "x", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	state.failNext.Store(true)
	_, err := productionProbe(state)
	if err == nil {
		t.Fatal("productionProbe: want error from refresh failure, got nil")
	}
}

func TestProductionProbeUsageError(t *testing.T) {
	state := &fakeRefreshableState{id: "x", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	orig := oauth.FetchUsageFn
	defer func() { oauth.FetchUsageFn = orig }()
	oauth.FetchUsageFn = func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Error: "HTTP 403"}
	}
	_, err := productionProbe(state)
	if err == nil {
		t.Fatal("productionProbe: want error from usage probe, got nil")
	}
}

func TestProductionProbeOk(t *testing.T) {
	state := &fakeRefreshableState{id: "x", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	orig := oauth.FetchUsageFn
	defer func() { oauth.FetchUsageFn = orig }()
	oauth.FetchUsageFn = func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{}
	}
	info, err := productionProbe(state)
	if err != nil {
		t.Fatalf("productionProbe: %v", err)
	}
	if info == nil {
		t.Fatal("info is nil")
	}
}

func TestSchedulerRunFiresOnTick(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	// Multi-entry pool so the singleton bypass doesn't short-circuit
	// runOnce — this test asserts Run() actually fires runOnce on
	// every tick, not anything singleton-specific.
	pool := &credPool{
		entries: map[string]*poolEntry{
			"a": {state: stateA, status: statusActivated},
			"b": {state: stateB, status: statusCandidate},
		},
		activated: "a",
		singleton: false,
	}
	var probeCalls atomic.Int32
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		probeCalls.Add(1)
		return &oauth.UsageInfo{}, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, time.Second)
	done := make(chan struct{})
	go sch.Run(done)

	// Wait for the goroutine to register its ticker.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		registered := len(fc.tickers) > 0
		fc.mu.Unlock()
		if registered {
			break
		}
		time.Sleep(time.Millisecond)
	}

	fc.Advance(2 * time.Second)
	// Wait for at least one probe call.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if probeCalls.Load() >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(done)
	if probeCalls.Load() < 1 {
		t.Errorf("probeCalls = %d, want >= 1", probeCalls.Load())
	}
}

func TestSchedulerRunStopsOnDone(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return &oauth.UsageInfo{}, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, time.Minute)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		sch.Run(done)
		close(exited)
	}()
	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not exit on done close")
	}
}

func TestSchedulerCaptureFailureSkipsRotation(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated, captured: http.Header{"X-Cred": []string{"a"}}},
		"b": {state: stateB, status: statusCandidate},
	}, activated: "a"}

	probes := map[string]*oauth.UsageInfo{
		"a": {Quotas: []oauth.Quota{{Name: "5h", Used: 90, ResetsAt: now.Add(4 * time.Hour).Format(time.RFC3339)}}},
		"b": {Quotas: []oauth.Quota{{Name: "5h", Used: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}},
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return probes[state.credID()], nil
	}

	origCapture := captureCredFn
	defer func() { captureCredFn = origCapture }()
	captureCalls := 0
	captureCredFn = func(_ *store.Credential, _ string) (http.Header, error) {
		captureCalls++
		return nil, errors.New("capture broken")
	}

	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()

	if pool.activated != "a" {
		t.Errorf("activated changed to %q despite capture failure (want a)", pool.activated)
	}
	if pool.entries["b"].status != statusCandidate {
		t.Errorf("b status = %v, want candidate (capture failure ≠ probe failure)", pool.entries["b"].status)
	}
	if pool.entries["b"].consecutiveFail != 0 {
		t.Errorf("b consecutiveFail = %d, want 0 (capture failure must NOT bump it)", pool.entries["b"].consecutiveFail)
	}
	if captureCalls != 1 {
		t.Errorf("captureCalls = %d, want 1", captureCalls)
	}
}

func TestSchedulerSuccessfulCaptureStoresHeadersAndPromotes(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated, captured: http.Header{"X-Cred": []string{"a"}}},
		"b": {state: stateB, status: statusCandidate},
	}, activated: "a"}

	probes := map[string]*oauth.UsageInfo{
		"a": {Quotas: []oauth.Quota{{Name: "5h", Used: 90, ResetsAt: now.Add(4 * time.Hour).Format(time.RFC3339)}}},
		"b": {Quotas: []oauth.Quota{{Name: "5h", Used: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}},
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return probes[state.credID()], nil
	}

	origCapture := captureCredFn
	defer func() { captureCredFn = origCapture }()
	captureCredFn = func(cred *store.Credential, _ string) (http.Header, error) {
		return http.Header{"X-Cred": []string{cred.ID}}, nil
	}

	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()

	if pool.activated != "b" {
		t.Errorf("activated = %q, want b", pool.activated)
	}
	if got := pool.entries["b"].captured.Get("X-Cred"); got != "b" {
		t.Errorf("b captured X-Cred = %q, want b", got)
	}
}

func TestSchedulerSkipCaptureRotatesWithoutCapture(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"a": {state: stateA, status: statusActivated},
		"b": {state: stateB, status: statusCandidate},
	}, activated: "a"}

	probes := map[string]*oauth.UsageInfo{
		"a": {Quotas: []oauth.Quota{{Name: "5h", Used: 90, ResetsAt: now.Add(4 * time.Hour).Format(time.RFC3339)}}},
		"b": {Quotas: []oauth.Quota{{Name: "5h", Used: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}},
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return probes[state.credID()], nil
	}

	captureCalls := 0
	origCapture := captureCredFn
	defer func() { captureCredFn = origCapture }()
	captureCredFn = func(_ *store.Credential, _ string) (http.Header, error) {
		captureCalls++
		return http.Header{}, nil
	}

	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.skipCapture = true
	sch.runOnce()

	if captureCalls != 0 {
		t.Errorf("captureCalls = %d, want 0 (skipCapture must short-circuit)", captureCalls)
	}
	if pool.activated != "b" {
		t.Errorf("activated = %q, want b", pool.activated)
	}
	if pool.entries["b"].captured != nil {
		t.Errorf("b.captured = %v, want nil (skipCapture path stores nil)", pool.entries["b"].captured)
	}
}

func TestSchedulerTickDoneSignalsAfterRunOnce(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
	}
	probeFn := func(_ poolEntryState) (*oauth.UsageInfo, error) {
		return &oauth.UsageInfo{}, nil
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)

	// runOnce should pulse tickDone exactly once.
	sch.runOnce()

	select {
	case <-sch.TickDone():
		// pulse received
	default:
		t.Fatal("TickDone did not pulse after runOnce")
	}
}

func TestClampTTL(t *testing.T) {
	cases := []struct {
		in, want time.Duration
	}{
		{30 * time.Second, 10 * time.Minute},
		{5 * time.Minute, 10 * time.Minute},
		{10 * time.Minute, 10 * time.Minute},
		{20 * time.Minute, 20 * time.Minute},
		{59 * time.Minute, 59 * time.Minute},
		{60 * time.Minute, 60 * time.Minute},
		{2 * time.Hour, time.Hour},
	}
	for _, tc := range cases {
		if got := clampTTL(tc.in); got != tc.want {
			t.Errorf("clampTTL(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestNewSchedulerTTLClamp(t *testing.T) {
	pool := &credPool{entries: map[string]*poolEntry{}}
	fc := newFakeClock(time.Now())

	// 30s interval → 5×30s = 150s → clamped to 10min floor
	sch := newScheduler(pool, nil, fc, 30*time.Second)
	if sch.ttl != 10*time.Minute {
		t.Errorf("30s interval: ttl=%s, want 10m", sch.ttl)
	}

	// 1h interval → 5×1h = 5h → clamped to 1h ceiling
	sch = newScheduler(pool, nil, fc, time.Hour)
	if sch.ttl != time.Hour {
		t.Errorf("1h interval: ttl=%s, want 1h", sch.ttl)
	}

	// 5m default → 25m, no clamp
	sch = newScheduler(pool, nil, fc, 5*time.Minute)
	if sch.ttl != 25*time.Minute {
		t.Errorf("5m interval: ttl=%s, want 25m", sch.ttl)
	}
}

func TestRunOnce_CacheHit_SkipsProbe(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries: map[string]*poolEntry{
			"a": {state: stateA, status: statusActivated,
				lastUsage: &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10}}},
				lastUsageAt: now,
			},
			"b": {state: stateB, status: statusCandidate,
				lastUsage: &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 50}}},
				lastUsageAt: now,
			},
		},
		activated: "a",
	}
	probeCalled := false
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		probeCalled = true
		t.Errorf("probe was called despite fresh cache")
		return nil, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, 5*time.Minute) // ttl=25m

	// Advance fake clock by 1 minute — well within 25m TTL.
	fc.Advance(1 * time.Minute)
	sch.runOnce()

	if probeCalled {
		t.Fatal("probe must not be called when cache fresh")
	}
}

func TestRunOnce_CacheMiss_ProbesAndUpdates(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries: map[string]*poolEntry{
			"a": {state: stateA, status: statusActivated,
				lastUsage: &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10}}},
				lastUsageAt: now.Add(-30 * time.Minute), // past 25m TTL
			},
			"b": {state: stateB, status: statusCandidate,
				lastUsage: &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 50}}},
				lastUsageAt: now.Add(-30 * time.Minute),
			},
		},
		activated: "a",
	}
	calls := 0
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		calls++
		return &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 5}}}, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, 5*time.Minute)

	sch.runOnce()

	if calls != 2 {
		t.Errorf("probe calls = %d, want 2 (both creds past TTL)", calls)
	}
}

func TestRunOnce_CacheMiss_ProbeFails_DoesNotAbortTick(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries: map[string]*poolEntry{
			"a": {state: stateA, status: statusActivated,
				lastUsage: &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10}}},
				lastUsageAt: now.Add(-30 * time.Minute),
			},
			"b": {state: stateB, status: statusCandidate,
				lastUsage: &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 50}}},
				lastUsageAt: now.Add(-30 * time.Minute),
			},
		},
		activated: "a",
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return nil, errors.New("blip")
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, 5*time.Minute)

	// Tick must not panic. consecutiveFail bumps for both creds.
	sch.runOnce()

	if pool.entries["a"].consecutiveFail < 1 {
		t.Errorf("a consecutiveFail = %d, want >= 1 after probe failure",
			pool.entries["a"].consecutiveFail)
	}
	if pool.entries["b"].consecutiveFail < 1 {
		t.Errorf("b consecutiveFail = %d, want >= 1 after probe failure",
			pool.entries["b"].consecutiveFail)
	}
}

func TestRunOnce_Singleton_NoProbeNoAlgorithm(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
	}
	probeCalled := false
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		probeCalled = true
		t.Error("probe was called on singleton pool")
		return nil, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, 5*time.Minute)

	sch.runOnce()

	if probeCalled {
		t.Fatal("singleton bypass failed: probe was called")
	}
	// tickDone must be pulsed even on bypass so test syncs don't hang.
	select {
	case <-sch.tickDone:
	case <-time.After(time.Second):
		t.Fatal("tickDone not pulsed on singleton bypass")
	}
	// activated unchanged
	if pool.activated != "a" {
		t.Errorf("activated = %q, want 'a'", pool.activated)
	}
}

func TestRunOnce_CacheMiss_ProbeFails_NilLastUsage(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "b", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries: map[string]*poolEntry{
			// activated has cached value; candidate has nil lastUsage.
			"a": {state: stateA, status: statusActivated,
				lastUsage:   &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10}}},
				lastUsageAt: now,
			},
			"b": {state: stateB, status: statusCandidate,
				lastUsage:   nil,
				lastUsageAt: time.Time{},
			},
		},
		activated: "a",
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		// b's probe fails; a's is a cache hit and not invoked.
		if state.credID() == "b" {
			return nil, errors.New("nope")
		}
		t.Errorf("probe called for %q (should be cache hit)", state.credID())
		return nil, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, 5*time.Minute)

	// Must not panic; the eligibility check filters out nil lastUsage.
	sch.runOnce()

	if pool.entries["b"].consecutiveFail < 1 {
		t.Errorf("b consecutiveFail = %d, want >= 1", pool.entries["b"].consecutiveFail)
	}
}
