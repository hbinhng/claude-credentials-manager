package share

import (
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

func ptrFloat(f float64) *float64 { return &f }

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
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
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
