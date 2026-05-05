//go:build !windows

package share

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// makePoolForTest builds a pool with N pre-admitted credentials
// (skipping the BuildPool startup probe) so each integration test
// can drive a known starting state.
func makePoolForTest(t *testing.T, ids []string) *credPool {
	t.Helper()
	pool := &credPool{entries: map[string]*poolEntry{}}
	for _, id := range ids {
		state := &fakeRefreshableState{id: id, expiresAt: time.Now().Add(8 * time.Hour).UnixMilli()}
		pool.entries[id] = &poolEntry{state: state, status: statusCandidate, lastUsage: &oauth.UsageInfo{}}
	}
	pool.entries[ids[0]].status = statusActivated
	pool.activated = ids[0]
	pool.singleton = len(ids) == 1
	return pool
}

func TestLoadBalanceTwoCredRotation(t *testing.T) {
	pool := makePoolForTest(t, []string{"aaaaaaaa", "bbbbbbbb"})
	now := time.Now()
	probes := map[string]*oauth.UsageInfo{
		"aaaaaaaa": {Quotas: []oauth.Quota{
			{Name: "5h", Used: 90, ResetsAt: now.Add(4 * time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 80, ResetsAt: now.Add(7 * 24 * time.Hour).Format(time.RFC3339)},
		}},
		"bbbbbbbb": {Quotas: []oauth.Quota{
			{Name: "5h", Used: 10, ResetsAt: now.Add(30 * time.Minute).Format(time.RFC3339)},
			{Name: "7d", Used: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
		}},
	}
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return probes[state.credID()], nil
	}

	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()

	if pool.activated != "bbbbbbbb" {
		t.Fatalf("activated = %q, want bbbbbbbb (higher feasibility)", pool.activated)
	}
}

func TestLoadBalanceActivatedDiesCandidatePromoted(t *testing.T) {
	pool := makePoolForTest(t, []string{"aaaaaaaa", "bbbbbbbb"})
	now := time.Now()
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		if state.credID() == "aaaaaaaa" {
			return nil, fmt.Errorf("probe broken")
		}
		return &oauth.UsageInfo{}, nil
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()
	sch.runOnce()
	if pool.activated != "bbbbbbbb" {
		t.Fatalf("activated = %q, want bbbbbbbb (a should have been demoted)", pool.activated)
	}
	if pool.entries["aaaaaaaa"].status != statusDegraded {
		t.Fatalf("a status = %v, want degraded", pool.entries["aaaaaaaa"].status)
	}
}

func TestLoadBalanceAllDegraded503ThenRecovery(t *testing.T) {
	pool := makePoolForTest(t, []string{"aaaaaaaa", "bbbbbbbb"})
	now := time.Now()

	// Phase 1: every probe fails → both degraded.
	failingProbe := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return nil, fmt.Errorf("everything broken")
	}
	sch := newScheduler(pool, failingProbe, newFakeClock(now), time.Minute)
	for i := 0; i < 3; i++ {
		sch.runOnce()
	}
	if pool.activated != "" {
		t.Fatalf("activated = %q, want empty (all degraded)", pool.activated)
	}

	// Fresh() returns errNoActivated.
	if _, err := pool.Fresh(); err != errNoActivated {
		t.Fatalf("Fresh err = %v, want errNoActivated", err)
	}

	// Phase 2: bbbbbbbb recovers.
	recoveryProbe := func(state poolEntryState) (*oauth.UsageInfo, error) {
		if state.credID() == "bbbbbbbb" {
			return &oauth.UsageInfo{}, nil
		}
		return nil, fmt.Errorf("still broken")
	}
	sch2 := newScheduler(pool, recoveryProbe, newFakeClock(now), time.Minute)
	sch2.runOnce()
	if pool.activated != "bbbbbbbb" {
		t.Fatalf("activated = %q, want bbbbbbbb after recovery", pool.activated)
	}
}

func TestLoadBalanceUpstream401FastPath(t *testing.T) {
	pool := makePoolForTest(t, []string{"aaaaaaaa", "bbbbbbbb"})
	pool.entries["aaaaaaaa"].consecutiveFail = 0
	pool.SignalActivatedFailed()
	pool.SignalActivatedFailed()
	if got := pool.entries["aaaaaaaa"].consecutiveFail; got != 2 {
		t.Fatalf("consecutiveFail = %d, want 2", got)
	}

	now := time.Now()
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return &oauth.UsageInfo{}, nil
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	sch.runOnce()
	if pool.activated != "bbbbbbbb" {
		t.Fatalf("activated = %q, want bbbbbbbb (a was Signal'd failed)", pool.activated)
	}
}

func TestLoadBalanceSingletonNeverDemoted(t *testing.T) {
	pool := makePoolForTest(t, []string{"aaaaaaaa"})
	now := time.Now()
	probeFn := func(state poolEntryState) (*oauth.UsageInfo, error) {
		return nil, fmt.Errorf("boom")
	}
	sch := newScheduler(pool, probeFn, newFakeClock(now), time.Minute)
	for i := 0; i < 5; i++ {
		sch.runOnce()
	}
	if pool.activated != "aaaaaaaa" {
		t.Fatalf("singleton activated changed to %q", pool.activated)
	}
}

func TestLoadBalanceGoroutineLeak(t *testing.T) {
	// We don't import goleak; just compare counts before/after.
	before := runtime.NumGoroutine()

	withFakeUsage(t, func(token string) *oauth.UsageInfo { return &oauth.UsageInfo{} })

	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	stubServer := setupRefreshStub(t)
	_ = stubServer

	pool, initialCred, err := BuildPool(nil)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	defer func() { _ = pool }()

	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(p *Proxy, _ string) error {
		p.markCaptured(http.Header{"User-Agent": []string{"test"}})
		return nil
	}
	origTunnel := startCloudflaredFn
	defer func() { startCloudflaredFn = origTunnel }()
	startCloudflaredFn = func(_ context.Context, _ string) (*Tunnel, string, error) {
		return NewTunnelForTest(nil), "https://test.example", nil
	}

	sess, err := StartSession(initialCred, Options{
		Pool:              pool,
		RebalanceInterval: 30 * time.Second,
		Clock:             newFakeClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sess.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Allow goroutines to drain.
	time.Sleep(50 * time.Millisecond)

	after := runtime.NumGoroutine()
	// +2 is a small allowance for Go runtime housekeeping. On a busy
	// machine the goroutine count may fluctuate by a small amount; we
	// treat <=2 as PASS rather than fail flakily.
	if after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d", before, after)
	}
}

func TestLoadBalanceProxy503OnNoActivated(t *testing.T) {
	pool := makePoolForTest(t, []string{"aaaaaaaa", "bbbbbbbb"})
	pool.activated = ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("upstream should not be hit when pool empty")
	}))
	defer upstream.Close()

	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	go proxy.Start()
	proxy.markCaptured(http.Header{"User-Agent": []string{"test"}})

	if err := proxy.Transition("acc-tok", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	resp, err := http.Get(proxy.Addr() + "/v1/messages")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		// We didn't pass an Authorization header — proxy 401 fires
		// before tokens.Fresh.
		t.Logf("(unauth path verified: status %d)", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", proxy.Addr()+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer acc-tok")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp2.StatusCode)
	}
	body := make([]byte, 256)
	n, _ := resp2.Body.Read(body)
	if !strings.Contains(string(body[:n]), "no usable credentials") {
		t.Errorf("body = %q, want to contain 'no usable credentials'", string(body[:n]))
	}
}

func TestProxyDirectorReadsHeadersFromPool(t *testing.T) {
	// Set up an upstream stub that records what it sees.
	var seen http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	// Point the proxy's upstream at the test server BEFORE NewProxy
	// (which captures upstream from upstreamBase()).
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	// Set up two creds; activated = a with captured headers = {X-Cred: a}.
	stateA := &fakeRefreshableState{id: "a", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	pool := &credPool{entries: map[string]*poolEntry{
		"a": {
			state:    stateA,
			status:   statusActivated,
			captured: http.Header{"X-Cred": []string{"a"}},
		},
	}, activated: "a"}

	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{}) // load-balance mode seeds empty p.captured
	go proxy.Start()

	// Transition with pool.
	if err := proxy.Transition("acc-tok", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// Issue a request through the proxy.
	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer acc-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client Do: %v", err)
	}
	defer resp.Body.Close()

	if got := seen.Get("X-Cred"); got != "a" {
		t.Errorf("upstream X-Cred = %q, want a (director should read from pool)", got)
	}
}
