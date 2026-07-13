//go:build !windows

// Package cmd — spec 2026-07-13-grok-support Task 11 acceptance tests for
// the grok upstream path. Mirrors the harness cmd/share_codex_acceptance_test.go
// already built (waitForProxy, decodeSessionTicket, postToSession are
// reused unmodified from that file — same package, same build tag).
//
// Grok differs from codex in two ways that shape this harness:
//   - Grok's terminal speaks plain net/http, not bogdanfinn/TLS, so the
//     fake upstream is a plain httptest.NewServer (see installGrokHandlersFake).
//   - Grok is single-active (never pooled), so every scenario here drives
//     the single-cred StartSession / NewLocalProxy path — there is no
//     load-balance variant to cover.
//
// Scenario coverage table:
//  S1 — grok launch with alias                    → TestGrokLaunch_WithAlias
//  S2 — grok share with alias                     → TestGrokShare_WithAlias
//  S3 — grok default model (no alias)              → TestGrokShare_DefaultModel
//  S4 — mid-session 401 refresh                    → TestGrokShare_MidSession401Refresh
//  S5 — die-fast on unknown model                  → TestGrokShare_DieFastOnUnknownModel
//  S6 — claude launch/share unaffected (regression) → TestClaudeLaunch_UnaffectedByGrokWiring,
//                                                      TestClaudeShare_UnaffectedByGrokWiring
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	grokoauth "github.com/hbinhng/claude-credentials-manager/internal/grok/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/hbinhng/claude-credentials-manager/internal/trace"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// installGrokHandlersFake installs a SetGrokHandlersFnForTest seam that
// returns GrokHandlers backed by the given plain-HTTP upstream. Unlike
// codex's bogdanfinn/TLS transport, grok's terminal uses a plain
// trace.Doer (net/http), so no TLS/cert setup is needed here.
func installGrokHandlersFake(t *testing.T, upstreamURL string) func() {
	t.Helper()
	doer := trace.WrapDoer(&http.Client{})
	return share.SetGrokHandlersFnForTest(func(cred *store.Credential) (share.GrokHandlers, error) {
		return share.GrokHandlers{Cred: cred, Transport: doer, UpstreamURL: upstreamURL}, nil
	})
}

// newGrokCred builds and stores a live (non-expired) grok credential.
// The fixed token pair lets tests assert on the exact bearer the
// terminal sends upstream.
func newGrokCred(t *testing.T, id, name string) *store.Credential {
	t.Helper()
	c := &store.Credential{
		ID:         id,
		Name:       name,
		Provider:   "grok",
		GrokTokens: &store.GrokTokens{AccessToken: "gk-access", RefreshToken: "gk-refresh"},
	}
	c.SetTokens("gk-access", "gk-refresh", time.Now().Add(time.Hour).UnixMilli())
	if err := store.Save(c); err != nil {
		t.Fatalf("store.Save grok cred: %v", err)
	}
	return c
}

// newExpiredGrokCred builds and stores a grok credential whose access
// token is already expired, forcing credState.Fresh() to take the
// credflow.RefreshFn path (and, through it, grokRefreshFn / SeamGrokRefresh)
// on the very first request.
func newExpiredGrokCred(t *testing.T, id, name, refreshToken string) *store.Credential {
	t.Helper()
	c := &store.Credential{
		ID:         id,
		Name:       name,
		Provider:   "grok",
		GrokTokens: &store.GrokTokens{AccessToken: "gk-stale-access", RefreshToken: refreshToken},
	}
	c.SetTokens("gk-stale-access", refreshToken, time.Now().Add(-time.Minute).UnixMilli())
	if err := store.Save(c); err != nil {
		t.Fatalf("store.Save expired grok cred: %v", err)
	}
	return c
}

// extractJSONField unmarshals body as a JSON object and returns the
// string value of the named top-level field ("" if absent or not a
// string). The caller performs the actual assertion.
func extractJSONField(t *testing.T, body []byte, field string) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("extractJSONField: parse body: %v; body=%s", err, body)
	}
	v, _ := parsed[field].(string)
	return v
}

// startSessionWithFakeGrokBackend starts a share.Session against the
// given fake grok upstream. It installs the grok handlers seam plus the
// same captureFn / cloudflared seams startSessionWithFakeCodexBackend
// uses, so no real `claude` binary or cloudflared process is needed.
func startSessionWithFakeGrokBackend(
	t *testing.T,
	cred *store.Credential,
	upstreamURL string,
	aliasRules []string,
) share.Session {
	t.Helper()

	restoreHandlers := installGrokHandlersFake(t, upstreamURL)
	t.Cleanup(restoreHandlers)

	share.SetCaptureFnForTest(func(p *share.Proxy, _ string) error {
		p.MarkCapturedForTest(http.Header{})
		return nil
	})
	t.Cleanup(share.ResetCaptureFnForTest)

	share.SetCloudflaredFnForTest(func(_ context.Context, _ string) (*share.Tunnel, string, error) {
		return share.NewTunnelForTest(nil), "https://grok-acceptance.example", nil
	})
	t.Cleanup(share.ResetCloudflaredFnForTest)

	var aliasMap *alias.Map
	if len(aliasRules) > 0 {
		m, err := alias.Parse(aliasRules)
		if err != nil {
			t.Fatalf("alias.Parse: %v", err)
		}
		aliasMap = m
	}

	sess, err := share.StartSession(cred, share.Options{
		BindHost:      "127.0.0.1",
		BindPort:      0,
		CapturePrompt: "acceptance",
		AliasMap:      aliasMap,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Stop() })

	waitForProxy(t, sess.Reach())
	return sess
}

// ── S1: grok launch with alias ───────────────────────────────────────────────

// TestGrokLaunch_WithAlias drives `ccm launch` for a grok credential via
// runLaunchLocal and proves it now routes through the grok Terminal
// (Task 13): the single-cred share.LocalProxy path builds a
// grokmw.Terminal via the same seamable grokHandlersFn factory the
// share pipeline uses, so installGrokHandlersFake points the terminal's
// UpstreamURL at the fake grok backend below. This asserts the fake
// GROK upstream — not upstreamBase() — receives the aliased model and
// the grok bearer, and that the response is relayed back to the client.
func TestGrokLaunch_WithAlias(t *testing.T) {
	setupHomeWithCcm(t)
	cred := newGrokCred(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01", "grok-launch-alias")

	var gotModel, gotAuth, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotModel = extractJSONField(t, b, "model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"hi from grok launch"}]}`))
	}))
	defer upstream.Close()

	// Route launch through the grok terminal at the fake upstream.
	defer installGrokHandlersFake(t, upstream.URL)()

	origAliases := launchModelAliases
	launchModelAliases = []string{"sonnet=grok-4.5"}
	t.Cleanup(func() { launchModelAliases = origAliases })

	var respBody atomic.Value
	var respStatus atomic.Int32
	restoreExec := share.SetLaunchExecFnForTest(func(name string, args []string, env []string) error {
		var baseURL string
		for _, e := range env {
			if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
				baseURL = strings.TrimPrefix(e, "ANTHROPIC_BASE_URL=")
			}
		}
		if baseURL == "" {
			return fmt.Errorf("no ANTHROPIC_BASE_URL in env")
		}
		waitForProxy(t, baseURL)
		req, _ := http.NewRequest("POST", baseURL+"/v1/messages",
			strings.NewReader(`{"model":"sonnet","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		respBody.Store(string(b))
		respStatus.Store(int32(resp.StatusCode))
		return nil
	})
	defer restoreExec()

	if err := runLaunchLocal(cred.ID, nil); err != nil {
		t.Fatalf("runLaunchLocal: %v", err)
	}

	if gotPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages (grok terminal path)", gotPath)
	}
	if gotModel != "grok-4.5" {
		t.Errorf("upstream model = %q, want grok-4.5", gotModel)
	}
	if gotAuth != "Bearer gk-access" {
		t.Errorf("upstream Authorization = %q, want Bearer gk-access", gotAuth)
	}
	if got := respStatus.Load(); got != http.StatusOK {
		t.Errorf("client status = %d, want 200", got)
	}
	if body, _ := respBody.Load().(string); !strings.Contains(body, "hi from grok launch") {
		t.Errorf("client body = %q, want relayed upstream response", body)
	}
}

// ── S2: grok share with alias ────────────────────────────────────────────────

// TestGrokShare_WithAlias drives the share.StartSession path (the
// engine behind `ccm share`) for a grok credential with a model alias,
// via the same startSessionWithFakeGrokBackend harness Task 10's
// share-package tests use, but at the cmd-level acceptance layer (real
// StartSession + real proxy HTTP round trip, not a Proxy unit test).
func TestGrokShare_WithAlias(t *testing.T) {
	setupHomeWithCcm(t)

	var gotModel, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotModel = extractJSONField(t, b, "model")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"hi from grok share"}]}`))
	}))
	defer upstream.Close()

	cred := newGrokCred(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb02", "grok-share-alias")

	sess := startSessionWithFakeGrokBackend(t, cred, upstream.URL, []string{"sonnet=grok-4.5"})

	inbound := `{"model":"sonnet","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	resp := postToSession(t, sess, inbound)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	if gotModel != "grok-4.5" {
		t.Errorf("upstream model = %q, want grok-4.5 (alias rewrite of sonnet)", gotModel)
	}
	if gotAuth != "Bearer gk-access" {
		t.Errorf("upstream Authorization = %q, want %q", gotAuth, "Bearer gk-access")
	}

	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "hi from grok share") {
		t.Errorf("downstream body = %q, want the fake upstream's response relayed through", out)
	}
}

// ── S3: default model (no alias) ─────────────────────────────────────────────

// TestGrokShare_DefaultModel verifies that with no alias rules
// configured (or none matching), the grok Terminal falls back to its
// DefaultModel constant, "grok-composer-2.5-fast", exactly.
func TestGrokShare_DefaultModel(t *testing.T) {
	setupHomeWithCcm(t)

	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotModel = extractJSONField(t, b, "model")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"default model reply"}]}`))
	}))
	defer upstream.Close()

	cred := newGrokCred(t, "cccccccc-cccc-cccc-cccc-cccccccccc03", "grok-default-model")

	sess := startSessionWithFakeGrokBackend(t, cred, upstream.URL, nil)

	inbound := `{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	resp := postToSession(t, sess, inbound)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	if gotModel != "grok-composer-2.5-fast" {
		t.Errorf("upstream model = %q, want the grok default model %q", gotModel, "grok-composer-2.5-fast")
	}
}

// ── S4: mid-session 401 refresh ──────────────────────────────────────────────

// TestGrokShare_MidSession401Refresh starts from an already-expired grok
// credential so the first BearerSrc.Fresh() call (inside the terminal's
// doWith401Retry) takes the credflow.RefreshFn path, which for a grok
// credential calls grokRefreshFn — installed here via
// credflow.SeamGrokRefresh to supply a rotated token instead of hitting
// xAI's real token endpoint. The fake upstream then 401s the first
// request regardless (simulating a race/late revocation) and 200s the
// retry, so this proves both refresh wiring and the terminal's
// retry-once-on-401 behavior.
func TestGrokShare_MidSession401Refresh(t *testing.T) {
	setupHomeWithCcm(t)

	var requestCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"type":"auth_error","message":"unauthorized"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"post-refresh ok"}]}`))
	}))
	defer upstream.Close()

	cred := newExpiredGrokCred(t, "dddddddd-dddd-dddd-dddd-dddddddddd04", "grok-401-refresh", "stale-refresh-token")

	var refreshCalls atomic.Int32
	restoreSeam := credflow.SeamGrokRefresh(func(refreshToken string) (*grokoauth.TokenResponse, error) {
		refreshCalls.Add(1)
		if refreshToken != "stale-refresh-token" {
			t.Errorf("SeamGrokRefresh received refresh token %q, want %q", refreshToken, "stale-refresh-token")
		}
		return &grokoauth.TokenResponse{
			AccessToken:  "rotated-access-token",
			RefreshToken: "rotated-refresh-token",
			ExpiresIn:    3600,
		}, nil
	})
	defer restoreSeam()

	sess := startSessionWithFakeGrokBackend(t, cred, upstream.URL, nil)

	inbound := `{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	resp := postToSession(t, sess, inbound)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d after 401+retry, want 200; body: %s", resp.StatusCode, body)
	}
	if n := requestCount.Load(); n != 2 {
		t.Errorf("upstream hit %d times, want 2 (1 401 + 1 retry)", n)
	}
	if n := refreshCalls.Load(); n != 1 {
		t.Errorf("SeamGrokRefresh invoked %d times, want exactly 1 (credState caches the rotated token for the retry)", n)
	}

	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "post-refresh ok") {
		t.Errorf("after 401 retry, response missing relayed content; got:\n%s", out)
	}
}

// ── S5: die-fast on unknown model ────────────────────────────────────────────

// TestGrokShare_DieFastOnUnknownModel verifies that when the grok
// upstream returns a model_not_found error, the client receives a 4xx
// response and the session's proxy shuts down (subsequent connections
// are refused) within a few seconds — mirroring the codex
// TestCodexShare_DieFastOnUnknownModel assertion shape.
func TestGrokShare_DieFastOnUnknownModel(t *testing.T) {
	setupHomeWithCcm(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"model_not_found","message":"unknown model 'grok-nope'"}}`)
	}))
	defer upstream.Close()

	cred := newGrokCred(t, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeee05", "grok-die-fast")

	sess := startSessionWithFakeGrokBackend(t, cred, upstream.URL, []string{"sonnet=grok-nope"})

	inbound := `{"model":"sonnet","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	resp := postToSession(t, sess, inbound)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck — drain before asserting

	if resp.StatusCode < 400 {
		t.Errorf("status = %d, want 4xx for model_not_found die-fast", resp.StatusCode)
	}

	proxyAddr := strings.TrimPrefix(sess.Reach(), "http://")
	deadline := time.Now().Add(3 * time.Second)
	proxyDown := false
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", proxyAddr, 100*time.Millisecond)
		if err != nil {
			proxyDown = true
			break
		}
		_ = conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	if !proxyDown {
		t.Error("proxy did not shut down within 3s after model_not_found die-fast")
	}
}

// ── S6: claude launch/share unaffected (regression) ──────────────────────────

// TestClaudeLaunch_UnaffectedByGrokWiring is the launch half of the S6
// regression check. It installs a grok credential and a
// SetGrokHandlersFnForTest spy that fails the test if ever invoked,
// then runs a plain claude launch and asserts (a) the spy was never
// called and (b) the claude launch exec still ran — proving grok's
// Task 9/10 wiring did not leak into (or break) the claude launch path.
func TestClaudeLaunch_UnaffectedByGrokWiring(t *testing.T) {
	setupHomeWithCcm(t)

	_ = newGrokCred(t, "ffffffff-ffff-ffff-ffff-ffffffffff06", "grok-bystander")
	restoreGrokSpy := share.SetGrokHandlersFnForTest(func(cred *store.Credential) (share.GrokHandlers, error) {
		t.Error("grokHandlersFn invoked during a claude launch; grok wiring leaked into the claude path")
		return share.GrokHandlers{}, fmt.Errorf("should not be called")
	})
	defer restoreGrokSpy()

	claudeCred := &store.Credential{
		ID:   "11111111-1111-1111-1111-111111111107",
		Name: "claude-launch-vs-grok-regression",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "claude-at",
			RefreshToken: "claude-rt",
			ExpiresAt:    time.Now().Add(6 * time.Hour).UnixMilli(),
		},
		CreatedAt:       "2026-05-09T00:00:00Z",
		LastRefreshedAt: "2026-05-09T00:00:00Z",
	}
	if err := store.Save(claudeCred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	share.SetUpstreamBaseForTest(upstream.URL)
	t.Cleanup(share.ResetUpstreamBaseForTest)

	var execCalled bool
	restoreExec := share.SetLaunchExecFnForTest(func(name string, args []string, env []string) error {
		execCalled = true
		return nil
	})
	defer restoreExec()

	if err := runLaunchLocal(claudeCred.ID, nil); err != nil {
		t.Fatalf("runLaunchLocal (claude): %v", err)
	}

	if !execCalled {
		t.Error("launch exec was not called; regression in claude launch path")
	}
}

// TestClaudeShare_UnaffectedByGrokWiring is the share half of the S6
// regression check: a claude share.StartSession must not route through
// grokHandlersFn either. It reuses the same spy pattern as the launch
// half above, but drives the actual share.StartSession entrypoint
// (the engine behind `ccm share`) instead of the launch LocalProxy.
func TestClaudeShare_UnaffectedByGrokWiring(t *testing.T) {
	setupHomeWithCcm(t)

	restoreGrokSpy := share.SetGrokHandlersFnForTest(func(cred *store.Credential) (share.GrokHandlers, error) {
		t.Error("grokHandlersFn invoked during a claude share session; grok wiring leaked into the claude path")
		return share.GrokHandlers{}, fmt.Errorf("should not be called")
	})
	defer restoreGrokSpy()

	claudeCred := &store.Credential{
		ID:   "22222222-2222-2222-2222-222222222208",
		Name: "claude-share-vs-grok-regression",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "claude-at",
			RefreshToken: "claude-rt",
			ExpiresAt:    time.Now().Add(6 * time.Hour).UnixMilli(),
		},
		CreatedAt:       "2026-05-09T00:00:00Z",
		LastRefreshedAt: "2026-05-09T00:00:00Z",
	}
	if err := store.Save(claudeCred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	share.SetCaptureFnForTest(func(p *share.Proxy, _ string) error {
		p.MarkCapturedForTest(http.Header{})
		return nil
	})
	t.Cleanup(share.ResetCaptureFnForTest)
	share.SetCloudflaredFnForTest(func(_ context.Context, _ string) (*share.Tunnel, string, error) {
		return share.NewTunnelForTest(nil), "https://claude-regression.example", nil
	})
	t.Cleanup(share.ResetCloudflaredFnForTest)

	sess, err := share.StartSession(claudeCred, share.Options{
		BindHost:      "127.0.0.1",
		BindPort:      0,
		CapturePrompt: "acceptance",
	})
	if err != nil {
		t.Fatalf("StartSession (claude): %v", err)
	}
	t.Cleanup(func() { _ = sess.Stop() })
	waitForProxy(t, sess.Reach())

	if sess.CredID() != claudeCred.ID {
		t.Errorf("CredID() = %q, want %q", sess.CredID(), claudeCred.ID)
	}
}
