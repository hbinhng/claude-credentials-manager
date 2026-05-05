//go:build !windows

package share

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// TestUsageCache_HeaderRefreshUpdatesCache drives a real request
// through the proxy and asserts that the unified-ratelimit headers
// on the upstream response refresh the active cred's lastUsage.
func TestUsageCache_HeaderRefreshUpdatesCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.20")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.74")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", "1778216400")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	pool := makePoolForTest(t, []string{"aaaaaaaa", "bbbbbbbb"})
	// Wipe lastUsage so the test can assert it gets populated.
	pool.entries["aaaaaaaa"].lastUsage = nil
	pool.entries["aaaaaaaa"].lastUsageAt = time.Time{}

	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{})
	go proxy.Start()
	if err := proxy.Transition("acc-tok", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer acc-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}

	// Wait briefly for ModifyResponse to complete its async update;
	// the lock pattern is synchronous within the response path but
	// the closure runs as part of the reverse-proxy goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pool.mu.RLock()
		got := pool.entries["aaaaaaaa"].lastUsage
		pool.mu.RUnlock()
		if got != nil && len(got.Quotas) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pool.mu.RLock()
	defer pool.mu.RUnlock()
	got := pool.entries["aaaaaaaa"]
	if got.lastUsage == nil {
		t.Fatal("lastUsage still nil after request — header path didn't fire")
	}
	if len(got.lastUsage.Quotas) != 2 {
		t.Errorf("expected 2 quotas (5h+7d), got %+v", got.lastUsage.Quotas)
	}
	if got.lastUsageAt.IsZero() {
		t.Errorf("lastUsageAt still zero")
	}
}

// TestUsageCache_TickSkipsProbeWhenCacheFresh asserts the scheduler
// short-circuits the HTTP probe when each entry's cached value is
// within the TTL window.
func TestUsageCache_TickSkipsProbeWhenCacheFresh(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "aaaaaaaa", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "bbbbbbbb", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries: map[string]*poolEntry{
			"aaaaaaaa": {state: stateA, status: statusActivated,
				lastUsage:   &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10}}},
				lastUsageAt: now,
			},
			"bbbbbbbb": {state: stateB, status: statusCandidate,
				lastUsage:   &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 50}}},
				lastUsageAt: now,
			},
		},
		activated: "aaaaaaaa",
	}

	probeCalled := atomic.Int32{}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		probeCalled.Add(1)
		return nil, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, 5*time.Minute) // ttl=25m

	// Advance fake clock by 1 minute (well within TTL); runOnce should
	// not call probe.
	fc.Advance(1 * time.Minute)
	sch.runOnce()

	if got := probeCalled.Load(); got != 0 {
		t.Errorf("probe called %d times, want 0 (cache fresh)", got)
	}
}

// TestUsageCache_TickProbesAfterTTLExpiry asserts probes resume once
// the cached entry is past the TTL.
func TestUsageCache_TickProbesAfterTTLExpiry(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "aaaaaaaa", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	stateB := &fakeRefreshableState{id: "bbbbbbbb", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries: map[string]*poolEntry{
			"aaaaaaaa": {state: stateA, status: statusActivated,
				lastUsage:   &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10}}},
				lastUsageAt: now.Add(-30 * time.Minute), // past 25m TTL
			},
			"bbbbbbbb": {state: stateB, status: statusCandidate,
				lastUsage:   &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 50}}},
				lastUsageAt: now.Add(-30 * time.Minute),
			},
		},
		activated: "aaaaaaaa",
	}

	probeCalled := atomic.Int32{}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		probeCalled.Add(1)
		return &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 5}}}, nil
	}
	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, 5*time.Minute)

	sch.runOnce()

	if got := probeCalled.Load(); got != 2 {
		t.Errorf("probe called %d times, want 2 (both past TTL)", got)
	}
}

// TestUsageCache_SingletonZeroProbes drives a singleton pool through
// 3 fake-clock ticks and asserts oauth.FetchUsageFn is NEVER called
// (admission skipped + tick bypass).
func TestUsageCache_SingletonZeroProbes(t *testing.T) {
	now := time.Now()
	stateA := &fakeRefreshableState{id: "a", expiresAt: now.Add(8 * time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
	}

	usageCalled := atomic.Int32{}
	prevUsage := oauth.FetchUsageFn
	oauth.FetchUsageFn = func(_ string) *oauth.UsageInfo {
		usageCalled.Add(1)
		return &oauth.UsageInfo{}
	}
	t.Cleanup(func() { oauth.FetchUsageFn = prevUsage })

	probeCalled := atomic.Int32{}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		probeCalled.Add(1)
		return &oauth.UsageInfo{}, nil
	}

	fc := newFakeClock(now)
	sch := newScheduler(pool, probeFn, fc, time.Minute)

	for i := 0; i < 3; i++ {
		sch.runOnce()
	}

	if got := usageCalled.Load(); got != 0 {
		t.Errorf("oauth.FetchUsageFn called %d times, want 0", got)
	}
	if got := probeCalled.Load(); got != 0 {
		t.Errorf("scheduler probe called %d times, want 0 (singleton bypass)", got)
	}
}

// TestUsageCache_HeaderResetsFailCounter asserts that a successful
// 200 response with valid headers clears the active cred's
// consecutiveFail counter.
func TestUsageCache_HeaderResetsFailCounter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.10")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.10")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", "1778216400")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	pool := makePoolForTest(t, []string{"aaaaaaaa", "bbbbbbbb"})
	pool.entries["aaaaaaaa"].consecutiveFail = 1

	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{})
	go proxy.Start()
	if err := proxy.Transition("acc-tok", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer acc-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pool.mu.RLock()
		got := pool.entries["aaaaaaaa"].consecutiveFail
		pool.mu.RUnlock()
		if got == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if got := pool.entries["aaaaaaaa"].consecutiveFail; got != 0 {
		t.Errorf("consecutiveFail = %d after 200, want 0", got)
	}
}

// TestUsageCache_SingleCred_HeaderUpdateGated asserts that for a
// non-pool (single-cred) session, the unified-ratelimit header parser
// is NEVER called — the outer `pool != nil` gate in the proxy's
// Transition closure must protect this.
func TestUsageCache_SingleCred_HeaderUpdateGated(t *testing.T) {
	parserCalled := atomic.Int32{}
	prevParser := parseRatelimitHeadersFn
	parseRatelimitHeadersFn = func(h http.Header) *oauth.UsageInfo {
		parserCalled.Add(1)
		t.Errorf("parser called for single-cred session")
		return prevParser(h)
	}
	t.Cleanup(func() { parseRatelimitHeadersFn = prevParser })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.10")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	// Single-cred mode: tokens is a credState, pool is nil.
	state := &fakeRefreshableState{id: "single", expiresAt: time.Now().Add(8 * time.Hour).UnixMilli()}
	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{})
	go proxy.Start()
	// pool == nil signals single-cred mode.
	if err := proxy.Transition("acc-tok", state, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer acc-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	// Settle. Parser must remain at 0.
	time.Sleep(100 * time.Millisecond)
	if got := parserCalled.Load(); got != 0 {
		t.Errorf("parser called %d times, want 0 (gated by pool != nil)", got)
	}
}
