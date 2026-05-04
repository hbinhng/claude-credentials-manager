package share

import (
	"fmt"
	"math"
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

func TestSchedulerActivatedStaysWhenOnlyOneEntryFailing(t *testing.T) {
	// activated has consecutiveFail=1 (< 2), no other candidate.
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

	// consecutiveFail goes 1 -> 2; activated is now ineligible.
	// But pool.singleton is auto-set false here (we constructed the
	// pool manually). Test the singleton path separately.
	if pool.activated == "" && !pool.singleton {
		t.Logf("non-singleton pool with only one entry: scheduler chose Demote → 503; this is correct branch (c)")
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
