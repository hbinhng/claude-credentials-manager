package share

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// stickyProxy wires a serving-mode Proxy with a sticky two-cred pool in
// front of the given fake-upstream handler. Returns the fronting test
// server; caller closes it and the proxy.
func stickyProxy(t *testing.T, upstream http.Handler, feasA, feasB float64) (*httptest.Server, *Proxy, *credPool) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	prev := SetUpstreamBaseForTest(up.URL)
	t.Cleanup(func() { SetUpstreamBaseForTest(prev) })

	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.accessToken = "secret"
	p.mode = modeServing

	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{token: "tokA"})
	eA.lastFeasibility = feasA
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastFeasibility = feasB
	pool := makePool("", false, map[string]*poolEntry{"a": eA, "b": eB})
	pool.enableSticky(newFakeClock(time.Unix(1_700_000_000, 0)))
	// markCaptured must be called before Transition (Transition returns
	// an error if p.captured == nil).
	p.markCaptured(http.Header{})
	if err := p.Transition("secret", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(srv.Close)
	return srv, p, pool
}

func TestStickyProxySessionStaysOnOneCred(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv, _, _ := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}), 100, 50)

	do := func(sid string) {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Claude-Code-Session-Id", sid)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
	}

	do(sidA)
	do(sidA)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("want 2 upstream hits, got %d", len(seen))
	}
	if seen[0] != "Bearer tokA" || seen[1] != "Bearer tokA" {
		t.Errorf("session must stick to tokA both times; got %v", seen)
	}
}

func TestStickyProxyRepinsAfterHardFailures(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv, _, _ := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()
		if auth == "Bearer tokA" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}), 100, 50)

	// 1st: picks a (best, fail=0) -> 401 -> fail=1, pin dropped.
	// 2nd: a has fail=1, excluded from new-session selection (strict guard:
	//      fail==0 required) -> picks b (clean) -> 200.
	// Bearer sequence is captured by the upstream handler's `seen` slice, so
	// stickyDo (which only differs in returning the status) is safe to use.
	_ = stickyDo(t, srv.URL, sidA)
	code := stickyDo(t, srv.URL, sidA)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("want 2 upstream hits, got %d: %v", len(seen), seen)
	}
	if seen[0] != "Bearer tokA" || seen[1] != "Bearer tokB" {
		t.Errorf("expected [tokA tokB], got %v", seen)
	}
	if code != http.StatusOK {
		t.Errorf("final request code = %d, want 200", code)
	}
}

func TestStickyProxyNoSessionIDStillServes(t *testing.T) {
	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}), 100, 50)

	// stickyDo with empty sid omits X-Claude-Code-Session-Id, matching the
	// sessionless-request behaviour the test exercises.
	if code := stickyDo(t, srv.URL, ""); code != http.StatusOK {
		t.Errorf("code = %d, want 200", code)
	}
	if len(pool.sessions) != 0 {
		t.Errorf("sessionless request must not create a pin; sessions=%v", pool.sessions)
	}
}

// stickyDo issues one authorized /v1/messages request for sid and returns
// the status code. Used by the 429-classification tests, which inspect the
// pin map rather than the upstream bearer.
func stickyDo(t *testing.T, srvURL, sid string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", srvURL+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer secret")
	if sid != "" {
		req.Header.Set("X-Claude-Code-Session-Id", sid)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestStickyProxyTransient429KeepsPin(t *testing.T) {
	// 429 with LOW utilization → transient rate-limit → pin kept.
	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.10")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", "1700003600")
		w.WriteHeader(http.StatusTooManyRequests)
	}), 100, 50)

	_ = stickyDo(t, srv.URL, sidA)

	if pool.sessions[sidA] == nil || pool.sessions[sidA].entryID != "a" {
		t.Errorf("transient 429 must keep the pin on a; got %+v", pool.sessions[sidA])
	}
}

func TestStickyProxyExhausted429DropsPin(t *testing.T) {
	// 429 with 100% utilization → quota-exhausted → pin dropped.
	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1.0")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", "1700003600")
		w.WriteHeader(http.StatusTooManyRequests)
	}), 100, 50)

	_ = stickyDo(t, srv.URL, sidA)

	if _, ok := pool.sessions[sidA]; ok {
		t.Errorf("quota-exhausted 429 must drop the pin; sessions=%v", pool.sessions)
	}
}

// stickyProxyDegraded builds a serving-mode Proxy whose sticky pool has
// only degraded entries so every routeSession call returns errNoActivated.
func stickyProxyDegraded(t *testing.T) (*httptest.Server, *credPool) {
	t.Helper()
	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.accessToken = "secret"
	p.mode = modeServing

	eA := newEntry("a", "alice", statusDegraded, &fakeTokenSource{token: "tokA"})
	eA.lastFeasibility = 100
	pool := makePool("", false, map[string]*poolEntry{"a": eA})
	pool.enableSticky(newFakeClock(time.Unix(1_700_000_000, 0)))
	p.markCaptured(http.Header{})
	if err := p.Transition("secret", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(srv.Close)
	return srv, pool
}

// TestStickyProxyNoEligible503 exercises the sticky handleServe error path
// where routeSession returns errNoActivated (all pool entries degraded).
// The proxy must respond with HTTP 503.
func TestStickyProxyNoEligible503(t *testing.T) {
	srv, _ := stickyProxyDegraded(t)

	if code := stickyDo(t, srv.URL, sidA); code != http.StatusServiceUnavailable {
		t.Errorf("all-degraded pool: code = %d, want 503", code)
	}
}

// TestStickyProxyFreshError502 exercises the sticky handleServe error path
// where routeSession succeeds but ts.Fresh() returns an error. The proxy
// must respond with HTTP 502.
func TestStickyProxyFreshError502(t *testing.T) {
	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.accessToken = "secret"
	p.mode = modeServing

	eA := newEntry("a", "alice", statusCandidate, &fakeTokenSource{err: errors.New("refresh boom")})
	eA.lastFeasibility = 100
	pool := makePool("", false, map[string]*poolEntry{"a": eA})
	pool.enableSticky(newFakeClock(time.Unix(1_700_000_000, 0)))
	p.markCaptured(http.Header{})
	if err := p.Transition("secret", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(srv.Close)

	if code := stickyDo(t, srv.URL, sidA); code != http.StatusBadGateway {
		t.Errorf("Fresh error: code = %d, want 502", code)
	}
}

// invalidGrantErr is a Fresh() error that errors.Is matches oauth.ErrInvalidGrant,
// wrapped the way the real refresh chain wraps it.
func invalidGrantErr() error {
	return fmt.Errorf("refresh: refresh failed (HTTP 400): {\"error\":\"invalid_grant\"}: %w", oauth.ErrInvalidGrant)
}

func TestStickyProxyInvalidGrantFlipsPin(t *testing.T) {
	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}), 100, 50)
	pool.entries["a"].state = &credStateAdapter{id: "a", name: "alice",
		src: &fakeTokenSource{err: invalidGrantErr()}}

	// invalid_grant on the best pick now fails over in-request → 200, pinned to b.
	if code := stickyDo(t, srv.URL, sidA); code != http.StatusOK {
		t.Fatalf("first call should fail over to b and 200; got %d", code)
	}
	if pool.entries["a"].status != statusDegraded {
		t.Errorf("a must be degraded after invalid_grant")
	}
	if pin := pool.sessions[sidA]; pin == nil || pin.entryID != "b" {
		t.Errorf("session must be pinned to healthy b; got %+v", pin)
	}
}

func TestShareSingleActiveInvalidGrantClearsAndWakes(t *testing.T) {
	prev := SetUpstreamBaseForTest("http://127.0.0.1:1") // unused on the error path
	t.Cleanup(func() { SetUpstreamBaseForTest(prev) })

	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.accessToken = "secret"
	p.mode = modeServing

	eA := newEntry("a", "alice", statusActivated, &fakeTokenSource{err: invalidGrantErr()})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastFeasibility = 50
	pool := makePool("a", false, map[string]*poolEntry{"a": eA, "b": eB}) // single-active, NOT sticky
	p.markCaptured(http.Header{})
	if err := p.Transition("secret", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(srv.Close)

	if code := stickyDo(t, srv.URL, sidA); code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", code)
	}
	if pool.entries["a"].status != statusDegraded {
		t.Errorf("a must be degraded")
	}
	if pool.activated != "" {
		t.Errorf("activated must be cleared; got %q", pool.activated)
	}
	select {
	case <-pool.wake:
	default:
		t.Error("wake must be signaled")
	}
}

func TestShareSingleCredInvalidGrantReLoginMessage(t *testing.T) {
	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.accessToken = "secret"
	p.mode = modeServing
	p.markCaptured(http.Header{})
	// Single-cred: tokens is a plain source, pool stays nil.
	if err := p.Transition("secret", &fakeTokenSource{err: invalidGrantErr()}, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", resp.StatusCode)
	}
	if !strings.Contains(string(body), "ccm login") {
		t.Errorf("single-cred invalid_grant body should mention `ccm login`; got %s", string(body))
	}
}

// TestStickyProxyPassthrough2xxNoteSuccess exercises the passthrough 2xx arm
// in ModifyResponse: noteSuccess must be called for passthrough entries so
// the session pin's lastSuccess is stamped and the cache-grace clock resets.
func TestStickyProxyPassthrough2xxNoteSuccess(t *testing.T) {
	// upstream is the real target the passthrough entry routes to.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	// upstreamHost is the host:port the passthrough ticket points to (no scheme).
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.accessToken = "secret"
	p.mode = modeServing

	tk := Ticket{Scheme: "http", Host: upstreamHost, Token: "ptok"}
	ptState := newPassthroughEntryState(tk)
	ptID := ptState.credID()
	eP := &poolEntry{
		state:           ptState,
		status:          statusCandidate,
		lastFeasibility: 100,
	}
	pool := makePool("", false, map[string]*poolEntry{ptID: eP})
	pool.enableSticky(newFakeClock(time.Unix(1_700_000_000, 0)))
	p.markCaptured(http.Header{})
	if err := p.Transition("secret", pool, pool); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(srv.Close)

	if code := stickyDo(t, srv.URL, sidA); code != http.StatusOK {
		t.Errorf("passthrough 2xx: code = %d, want 200", code)
	}

	// Verify the session was pinned to the passthrough entry and noteSuccess
	// ran (lastSuccess was stamped, i.e. pin exists with the correct entryID).
	pool.mu.RLock()
	pin := pool.sessions[sidA]
	pool.mu.RUnlock()
	if pin == nil {
		t.Fatal("passthrough 2xx must create a session pin")
	}
	if pin.entryID != ptID {
		t.Errorf("pin.entryID = %q, want %q", pin.entryID, ptID)
	}
	if pin.lastSuccess.IsZero() {
		t.Error("noteSuccess must stamp pin.lastSuccess for passthrough entries")
	}
}

func TestStickyProxyFirstCallFailsOverPastDeadAccount(t *testing.T) {
	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}), 100, 50)
	// Best pick "a" (feas 100) is dead; "b" (feas 50) is healthy.
	pool.entries["a"].state = &credStateAdapter{id: "a", name: "alice",
		src: &fakeTokenSource{err: invalidGrantErr()}}

	// A brand-new session's first call must transparently fail over to b → 200.
	if code := stickyDo(t, srv.URL, sidA); code != http.StatusOK {
		t.Fatalf("first call must fail over to a healthy account (200); got %d", code)
	}
	if pool.entries["a"].status != statusDegraded {
		t.Errorf("dead account a must be degraded")
	}
	pin := pool.sessions[sidA]
	if pin == nil || pin.entryID != "b" {
		t.Errorf("session must be pinned to healthy b; got %+v", pin)
	}
	// The dead account was never committed: the only pin is b.
	if pin != nil && pin.entryID == "a" {
		t.Error("dead account a must never have been pinned")
	}
}

func TestStickyProxyAllDeadReturns503(t *testing.T) {
	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), 100, 50)
	pool.entries["a"].state = &credStateAdapter{id: "a", name: "alice", src: &fakeTokenSource{err: invalidGrantErr()}}
	pool.entries["b"].state = &credStateAdapter{id: "b", name: "bob", src: &fakeTokenSource{err: invalidGrantErr()}}

	if code := stickyDo(t, srv.URL, sidA); code != http.StatusServiceUnavailable {
		t.Fatalf("all-dead pool must 503; got %d", code)
	}
	if len(pool.sessions) != 0 {
		t.Errorf("no pin should be committed when every candidate is dead; got %v", pool.sessions)
	}
}

func TestStickyProxyTransientOnFreshSelectionNoPin(t *testing.T) {
	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), 100, 50)
	// "a" (best) returns a TRANSIENT (non-invalid_grant) refresh error.
	pool.entries["a"].state = &credStateAdapter{id: "a", name: "alice",
		src: &fakeTokenSource{err: errors.New("refresh: network blip")}}

	if code := stickyDo(t, srv.URL, sidA); code != http.StatusBadGateway {
		t.Fatalf("transient Fresh error must 502 (no failover); got %d", code)
	}
	if _, ok := pool.sessions[sidA]; ok {
		t.Error("a transient failure on a fresh selection must NOT commit a pin")
	}
	if pool.entries["a"].status == statusDegraded {
		t.Error("a transient failure must NOT degrade the entry")
	}
}

func TestStickyProxyFailoverLogsPinOnlyForHealthy(t *testing.T) {
	orig := errLog
	var buf bytes.Buffer
	errLog = func() io.Writer { return &buf }
	defer func() { errLog = orig }()

	srv, _, pool := stickyProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}), 100, 50)
	pool.entries["a"].state = &credStateAdapter{id: "a", name: "alice",
		src: &fakeTokenSource{err: invalidGrantErr()}}

	_ = stickyDo(t, srv.URL, sidA)

	out := buf.String()
	if !strings.Contains(out, "sticky pin") || !strings.Contains(out, "bob") {
		t.Errorf("must log the committed pin to healthy bob; got %q", out)
	}
	if strings.Contains(out, "sticky pin "+shortID(sidA)+" -> alice") {
		t.Errorf("must NOT log a pin to the dead account alice; got %q", out)
	}
}
