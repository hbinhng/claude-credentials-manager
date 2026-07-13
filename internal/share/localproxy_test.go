package share

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
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

func localInvalidGrantErr() error {
	return fmt.Errorf("refresh: refresh failed (HTTP 400): {\"error\":\"invalid_grant\"}: %w", oauth.ErrInvalidGrant)
}

func TestLaunchPoolInvalidGrantClearsActivatedAndWakes(t *testing.T) {
	eA := newEntry("a", "alice", statusActivated, &fakeTokenSource{err: localInvalidGrantErr()})
	eA.lastFeasibility = 100
	eB := newEntry("b", "bob", statusCandidate, &fakeTokenSource{token: "tokB"})
	eB.lastFeasibility = 50
	pool := makePool("a", false, map[string]*poolEntry{"a": eA, "b": eB})
	lp := &LocalProxy{tokens: pool, pool: pool}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	lp.serveWithToken(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rr.Code)
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

func TestLaunchSingleCredInvalidGrantReLoginMessage(t *testing.T) {
	lp := &LocalProxy{tokens: &fakeTokenSource{err: localInvalidGrantErr()}} // pool nil
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	lp.serveWithToken(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "ccm login") {
		t.Errorf("single-cred invalid_grant body should mention `ccm login`; got %s", rr.Body.String())
	}
}

// TestNewLocalProxyGrokHandlersError verifies that when grokHandlersFn
// fails, NewLocalProxy propagates a wrapped error (and closes the
// partially-built proxy) instead of returning a LocalProxy whose launch
// requests would silently fall through to the Anthropic passthrough.
// Exercises setupProviderTerminal's grok error branch and the
// "setupProviderTerminal failed -> Close + propagate" branch NewLocalProxy
// added around it (Task 13).
func TestNewLocalProxyGrokHandlersError(t *testing.T) {
	restore := SetGrokHandlersFnForTest(func(cred *store.Credential) (GrokHandlers, error) {
		return GrokHandlers{}, errors.New("grok handlers boom")
	})
	defer restore()

	cred := grokCred(t, "aaaa1111-0000-0000-0000-localproxy001")

	lp, err := NewLocalProxy(cred)
	if err == nil {
		if lp != nil {
			_ = lp.Close()
		}
		t.Fatal("NewLocalProxy succeeded; want grok handlers error")
	}
	if !strings.Contains(err.Error(), "grok handlers") {
		t.Errorf("err = %v, want to contain %q", err, "grok handlers")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want wrapped cause to survive", err)
	}
}

// TestNewLocalProxyCodexHandlersError is the codex mirror of
// TestNewLocalProxyGrokHandlersError: codexHandlersFn failing must
// likewise abort NewLocalProxy with a wrapped error, exercising
// setupProviderTerminal's codex error branch.
func TestNewLocalProxyCodexHandlersError(t *testing.T) {
	restore := SetCodexHandlersFnForTest(func(cred *store.Credential) (CodexHandlers, error) {
		return CodexHandlers{}, errors.New("codex handlers boom")
	})
	defer restore()

	setupFakeHome(t)
	cred := mkCodexCred(t, "bbbb2222-0000-0000-0000-localproxy002")

	lp, err := NewLocalProxy(cred)
	if err == nil {
		if lp != nil {
			_ = lp.Close()
		}
		t.Fatal("NewLocalProxy succeeded; want codex handlers error")
	}
	if !strings.Contains(err.Error(), "codex handlers") {
		t.Errorf("err = %v, want to contain %q", err, "codex handlers")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want wrapped cause to survive", err)
	}
}

// TestNewLocalProxyGrokDieFastClosesProxy proves the onDie closure wired
// in setupProviderTerminal (`func(string) { go p.Close() }`) actually
// closes the LocalProxy when the grok terminal signals model_not_found —
// the same die-fast contract share.Proxy enforces via
// terminalForProvider/OnSessionDie (see TestGrokShare_DieFastOnUnknownModel
// in cmd/share_grok_acceptance_test.go), now reachable through `ccm
// launch` too.
func TestNewLocalProxyGrokDieFastClosesProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"model_not_found","message":"unknown model 'grok-nope'"}}`)
	}))
	defer upstream.Close()

	restore := SetGrokHandlersFnForTest(func(cred *store.Credential) (GrokHandlers, error) {
		return GrokHandlers{Cred: cred, Transport: http.DefaultClient, UpstreamURL: upstream.URL}, nil
	})
	defer restore()

	cred := grokCred(t, "cccc3333-0000-0000-0000-localproxy003")
	lp, err := NewLocalProxy(cred)
	if err != nil {
		t.Fatalf("NewLocalProxy: %v", err)
	}
	defer lp.Close()

	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages",
		strings.NewReader(`{"model":"grok-nope","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	addr := strings.TrimPrefix(lp.Addr(), "http://")
	deadline := time.Now().Add(3 * time.Second)
	down := false
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr != nil {
			down = true
			break
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	if !down {
		t.Error("proxy did not close within 3s after grok model_not_found die-fast")
	}
}
