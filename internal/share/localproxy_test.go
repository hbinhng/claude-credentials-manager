package share

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestNewLocalProxyPreservesSingleCredAPI(t *testing.T) {
	cred := &store.Credential{
		ID:            "11111111-1111-1111-1111-111111111111",
		ClaudeAiOauth: store.OAuthTokens{AccessToken: "tok"},
	}
	lp, err := NewLocalProxy(cred)
	if err != nil {
		t.Fatalf("NewLocalProxy: %v", err)
	}
	defer lp.Close()
	if lp.tokens == nil {
		t.Errorf("tokens is nil after NewLocalProxy(cred)")
	}
	if lp.pool != nil {
		t.Errorf("pool = %v, want nil for single-cred mode", lp.pool)
	}
}

func TestNewLocalProxyWithPoolBasics(t *testing.T) {
	stateA := &fakeRefreshableState{id: "a", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
	}
	lp, err := NewLocalProxyWithPool(pool, false)
	if err != nil {
		t.Fatalf("NewLocalProxyWithPool: %v", err)
	}
	defer lp.Close()
	if lp.pool != pool {
		t.Errorf("pool field not wired")
	}
	if lp.tokens == nil {
		t.Errorf("tokens not wired (should equal pool)")
	}
	if lp.rp.ModifyResponse == nil {
		t.Errorf("ModifyResponse hook not installed")
	}
}

func TestNewLocalProxyWithPoolNilPool(t *testing.T) {
	if _, err := NewLocalProxyWithPool(nil, false); err == nil {
		t.Errorf("NewLocalProxyWithPool(nil) should error")
	}
}

func TestNewLocalProxyNilCred(t *testing.T) {
	if _, err := NewLocalProxy(nil); err == nil {
		t.Errorf("NewLocalProxy(nil) should error")
	}
}

func TestLocalProxyHandle503OnNoActivated(t *testing.T) {
	pool := &credPool{entries: map[string]*poolEntry{}, activated: ""}
	lp, err := NewLocalProxyWithPool(pool, false)
	if err != nil {
		t.Fatalf("NewLocalProxyWithPool: %v", err)
	}
	defer lp.Close()
	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no usable credentials") {
		t.Errorf("body = %q, want to contain 'no usable credentials'", string(body))
	}
}

func TestLocalProxyModifyResponseSignalsActivatedFailed(t *testing.T) {
	stateA := &fakeRefreshableState{id: "aaaaaaaa", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"aaaaaaaa": {state: stateA, status: statusActivated}},
		activated: "aaaaaaaa",
	}

	// Stub upstream that returns 401.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	prev := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prev }()

	lp, err := NewLocalProxyWithPool(pool, false)
	if err != nil {
		t.Fatalf("NewLocalProxyWithPool: %v", err)
	}
	defer lp.Close()

	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := pool.entries["aaaaaaaa"].consecutiveFail; got != 1 {
		t.Errorf("consecutiveFail after upstream 401 = %d, want 1", got)
	}
}

func TestStartPoolBackgroundGoroutineLeak(t *testing.T) {
	stateA := &fakeRefreshableState{id: "a", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"a": {state: stateA, status: statusActivated}},
		activated: "a",
		singleton: true,
	}
	lp, err := NewLocalProxyWithPool(pool, false)
	if err != nil {
		t.Fatalf("NewLocalProxyWithPool: %v", err)
	}
	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	before := runtime.NumGoroutine()
	if err := StartPoolBackground(lp.Done(), pool, PoolBackgroundOptions{
		RebalanceInterval: time.Minute, SkipCapture: true,
	}); err != nil {
		t.Fatalf("StartPoolBackground: %v", err)
	}
	// Confirm goroutines spawned.
	if got := runtime.NumGoroutine(); got <= before {
		t.Errorf("no goroutines spawned (before=%d, after=%d)", before, got)
	}
	if err := lp.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Allow goroutines to drain. Tolerance is +2 because background
	// HTTP transport idle-conn goroutines may linger briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Errorf("goroutine leak: before=%d after-Close=%d", before, got)
	}
}

// waitForListener polls the address until it accepts a TCP
// connection, or fails the test after 1s. Removes the
// goroutine-startup race in tests that issue HTTP requests
// immediately after `go lp.Start()`.
func waitForListener(t *testing.T, addr string) {
	t.Helper()
	target := strings.TrimPrefix(addr, "http://")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", target)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("listener at %s did not become ready within 1s", addr)
}
