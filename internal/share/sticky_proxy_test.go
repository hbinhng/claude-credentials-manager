package share

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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

	do := func(sid string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Claude-Code-Session-Id", sid)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// 1st: picks a (best) -> 401 -> fail 1, pin dropped.
	// 2nd: re-picks a (still best, fail<2) -> 401 -> fail 2 -> a degraded.
	// 3rd: a degraded -> picks b -> 200.
	_ = do(sidA)
	_ = do(sidA)
	code := do(sidA)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("want 3 upstream hits, got %d: %v", len(seen), seen)
	}
	if seen[0] != "Bearer tokA" || seen[1] != "Bearer tokA" || seen[2] != "Bearer tokB" {
		t.Errorf("expected [tokA tokA tokB], got %v", seen)
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

	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer secret")
	// no X-Claude-Code-Session-Id
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("code = %d, want 200", resp.StatusCode)
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
