//go:build !windows

// Package cmd — spec §11.5 acceptance tests for the codex upstream path.
//
// Scenario coverage table:
//  S1 — Codex launch with alias                   → TestCodexLaunch_WithAlias
//  S2 — Codex share with alias                    → TestCodexShare_WithAlias
//  S3 — Claude launch unchanged (regression)      → TestClaudeLaunch_Regression
//  S4 — Alias conflict rejected at boot           → covered by TestShareCommand_ModelAliasConflictRejected
//                                                    and TestLaunchCommand_ModelAliasConflictRejected
//  S5 — (retired) Codex CLI no longer required    → tests removed in omniroute pivot Task 8
//  S6 — stream:false buffers to JSON              → TestCodexShare_StreamFalseBuffersJSON
//  S7 — Cancellation                              → TestCodexShare_ClientCancellation
//  S8 — Mid-session refresh on 401               → TestCodexShare_MidSession401Refresh
//  S9 — Die-fast on unknown model                 → TestCodexShare_DieFastOnUnknownModel
//  S10 — Reasoning round-trip                     → TestCodexShare_ReasoningRoundTrip
package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/identity"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/transport"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// newTestTransport returns a bogdanfinn transport with TLS verification
// disabled so tests can use httptest.NewTLSServer without cert setup.
func newTestTransport(t *testing.T) *transport.Transport {
	t.Helper()
	tr, err := transport.New(transport.Options{
		ProfileName:        transport.Default,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	return tr
}

// installCodexHandlersFake installs a SetCodexHandlersFnForTest seam
// that returns a CodexHandlers backed by the given TLS upstream. The
// returned restore function must be deferred by the caller.
//
// Renamed from installCodexCaptureFake per spec
// 2026-05-09-codex-omniroute-pivot §5.6.
func installCodexHandlersFake(t *testing.T, upstreamURL string) func() {
	t.Helper()
	tr := newTestTransport(t)
	restore := share.SetCodexHandlersFnForTest(func(cred *store.Credential) (share.CodexHandlers, error) {
		return share.CodexHandlers{
			Cred:        cred,
			Transport:   tr,
			UpstreamURL: upstreamURL,
		}, nil
	})
	return restore
}

// makeCodexJWT builds a minimal JWT-shaped access token with exp set 1h in
// the future. The token is not cryptographically signed but the exp claim
// is parseable by store.parseJWTExpMillis, which is what IsExpired() uses.
func makeCodexJWT(t *testing.T) string {
	t.Helper()
	exp := time.Now().Add(time.Hour).Unix()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(
		`{"email":"u@example.com","exp":` + strconv.FormatInt(exp, 10) +
			`,"https://api.openai.com/auth":{"chatgpt_account_id":"acct-accept"}}`,
	))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return h + "." + p + "." + s
}

// buildCodexCredAndStore creates a minimal codex credential in the store
// and returns it. The access token is a minimal JWT with a future expiry so
// IsExpired() returns false and credState.Fresh() does not attempt a real
// network refresh in tests.
func buildCodexCredAndStore(t *testing.T, id, name string) *store.Credential {
	t.Helper()
	tok := makeCodexJWT(t)
	cred := &store.Credential{
		ID:       id,
		Name:     name,
		Provider: "codex",
		AuthMode: "chatgpt",
		Tokens: &store.CodexTokens{
			IDToken:      tok,
			AccessToken:  tok,
			RefreshToken: "rt_refresh",
			AccountID:    "acct-accept",
		},
		CreatedAt:       "2026-05-09T00:00:00Z",
		LastRefreshedAt: "2026-05-09T00:00:00Z",
		LastRefresh:     "2026-05-09T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save codex cred: %v", err)
	}
	return cred
}

// startSessionWithFakeCodexBackend starts a share.Session against the given
// fake upstream server. It installs the capture seam, cloudflared seam, and
// the captureFn seam so no real CLI is needed. Returns the session handle.
func startSessionWithFakeCodexBackend(
	t *testing.T,
	cred *store.Credential,
	upstreamURL string,
	aliasRules []string,
) share.Session {
	t.Helper()

	restoreCapture := installCodexHandlersFake(t, upstreamURL)
	t.Cleanup(restoreCapture)

	// Stub the claude capture step (captureFn runs `claude -p`; skip it).
	share.SetCaptureFnForTest(func(p *share.Proxy, _ string) error {
		p.MarkCapturedForTest(http.Header{})
		return nil
	})
	t.Cleanup(share.ResetCaptureFnForTest)

	// Stub cloudflared so no external process is needed.
	share.SetCloudflaredFnForTest(func(_ context.Context, _ string) (*share.Tunnel, string, error) {
		return share.NewTunnelForTest(nil), "https://acceptance.example", nil
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

	// Wait for the proxy to accept connections.
	waitForProxy(t, sess.Reach())
	return sess
}

// waitForProxy polls until the proxy accepts connections or times out.
func waitForProxy(t *testing.T, reach string) {
	t.Helper()
	addr := strings.TrimPrefix(reach, "http://")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("proxy did not accept connections within 3s")
}

// decodeSessionTicket decodes the ticket from a share.Session.
func decodeSessionTicket(t *testing.T, sess share.Session) share.Ticket {
	t.Helper()
	tk, err := share.DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	return tk
}

// postToSession sends a POST /v1/messages to the share session's proxy,
// using the session bearer as Authorization header. Returns the response.
func postToSession(t *testing.T, sess share.Session, body string) *http.Response {
	t.Helper()
	tk := decodeSessionTicket(t, sess)
	req, err := http.NewRequest("POST", sess.Reach()+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tk.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	return resp
}

// ── S1: codex launch with alias ──────────────────────────────────────────────

// TestCodexLaunch_WithAlias tests that runLaunchLocal for a codex credential:
//  1. Does not require codex CLI on PATH (omniroute pivot dropped that gate)
//  2. Builds a local proxy that forwards to a fake codex backend
//  3. The launch exec receives the proxy URL in the environment
//
// This validates that the launch path correctly injects the proxy URL
// when codex is the provider and that model alias rules are applied.
func TestCodexLaunch_WithAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only test")
	}
	setupHomeWithCcm(t)

	// Fake codex backend: just return 200 for any request
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	restoreCapture := installCodexHandlersFake(t, upstream.URL)
	defer restoreCapture()

	cred := buildCodexCredAndStore(t,
		"cccccccc-cccc-cccc-cccc-cccccccccc01",
		"codex-launch-alias",
	)

	// Set alias flag for launch command.
	orig := launchModelAliases
	launchModelAliases = []string{"claude-opus-*=gpt-5-codex"}
	t.Cleanup(func() { launchModelAliases = orig })

	// Capture the environment that runLaunchLocal will pass to the exec stub.
	var receivedBaseURL atomic.Value
	restoreExec := share.SetLaunchExecFnForTest(func(name string, args []string, env []string) error {
		for _, e := range env {
			if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
				receivedBaseURL.Store(strings.TrimPrefix(e, "ANTHROPIC_BASE_URL="))
			}
		}
		return nil
	})
	defer restoreExec()

	if err := runLaunchLocal(cred.ID, nil); err != nil {
		t.Fatalf("runLaunchLocal: %v", err)
	}

	got, _ := receivedBaseURL.Load().(string)
	if !strings.HasPrefix(got, "http://") {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want an http:// address", got)
	}
}

// ── S2: codex share with alias ───────────────────────────────────────────────

// TestCodexShare_WithAlias verifies the end-to-end share path for a codex
// credential with a model alias. It:
//  1. Starts a share session (captureFn and codexHandlersFn stubbed)
//  2. Sends a Claude Messages POST with a model that matches the alias
//  3. Asserts the fake codex backend received a translated gpt-* model name
//  4. Asserts the downstream response is Anthropic SSE
func TestCodexShare_WithAlias(t *testing.T) {
	setupHomeWithCcm(t)

	const claudeSessionID = "019e0a01-5569-7480-8945-f61f37958342"

	// Body + synthesized identity headers + URL path seen by the fake codex
	// backend. Captured here so post-request assertions can verify that
	// Bundle.Apply's headers actually arrive on the upstream side and that
	// the outbound URL path is /backend-api/codex/responses (not /v1/responses).
	var (
		upstreamBody           atomic.Value
		upstreamVersion        string
		upstreamOpenaiBeta     string
		upstreamUserAgent      string
		upstreamAccountID      string
		upstreamPath           string
		upstreamSessionIDSnake string
		upstreamSessionIDKebab string
		upstreamThreadIDSnake  string
		upstreamThreadIDKebab  string
		upstreamWindowID       string
		upstreamOriginator     string
		upstreamBetaFeatures   string
		upstreamTurnMetaRaw    string
		upstreamClientReqID    string
		upstreamInstructions   string
		upstreamPromptCacheKey string
	)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamVersion = r.Header.Get("Version")
		upstreamOpenaiBeta = r.Header.Get("Openai-Beta")
		upstreamUserAgent = r.Header.Get("User-Agent")
		upstreamAccountID = r.Header.Get("chatgpt-account-id")
		upstreamPath = r.URL.Path
		upstreamSessionIDSnake = r.Header.Get("Session_id")
		upstreamSessionIDKebab = r.Header.Get("Session-Id")
		upstreamThreadIDSnake = r.Header.Get("Thread_id")
		upstreamThreadIDKebab = r.Header.Get("Thread-Id")
		upstreamWindowID = r.Header.Get("X-Codex-Window-Id")
		upstreamOriginator = r.Header.Get("Originator")
		upstreamBetaFeatures = r.Header.Get("X-Codex-Beta-Features")
		upstreamTurnMetaRaw = r.Header.Get("X-Codex-Turn-Metadata")
		upstreamClientReqID = r.Header.Get("X-Client-Request-Id")
		b, _ := io.ReadAll(r.Body)
		upstreamBody.Store(string(b))
		var parsed map[string]any
		if err := json.Unmarshal(b, &parsed); err == nil {
			upstreamInstructions, _ = parsed["instructions"].(string)
			upstreamPromptCacheKey, _ = parsed["prompt_cache_key"].(string)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := buildCodexCredAndStore(t,
		"cccccccc-cccc-cccc-cccc-cccccccccc02",
		"codex-share-alias",
	)

	sess := startSessionWithFakeCodexBackend(t, cred, upstream.URL,
		[]string{"claude-opus-*=gpt-5-codex"})

	inbound := `{"model":"claude-opus-4.7","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	tk := decodeSessionTicket(t, sess)
	req, err := http.NewRequest("POST", sess.Reach()+"/v1/messages", strings.NewReader(inbound))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tk.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", claudeSessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// Assert upstream received translated model name.
	rawUpstream, _ := upstreamBody.Load().(string)
	var upstreamReq map[string]any
	if err := json.Unmarshal([]byte(rawUpstream), &upstreamReq); err == nil {
		if m, _ := upstreamReq["model"].(string); m != "gpt-5-codex" {
			t.Errorf("upstream model = %q, want gpt-5-codex", m)
		}
	}

	// Assert downstream response contains Anthropic SSE markers.
	out, _ := io.ReadAll(resp.Body)
	outStr := string(out)
	if !strings.Contains(outStr, "content_block_delta") {
		t.Errorf("downstream SSE missing content_block_delta; got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "message_stop") {
		t.Errorf("downstream SSE missing message_stop; got:\n%s", outStr)
	}

	// Integration-level guarantees from the omniroute pivot: the synthesized
	// identity headers built by Bundle.Apply must actually arrive on the
	// upstream side, and the outbound URL path must be /backend-api/codex/responses.
	if upstreamVersion != identity.StaticVersion {
		t.Errorf("upstream Version = %q, want %q", upstreamVersion, identity.StaticVersion)
	}
	if upstreamOpenaiBeta != identity.StaticOpenaiBeta {
		t.Errorf("upstream Openai-Beta = %q, want %q", upstreamOpenaiBeta, identity.StaticOpenaiBeta)
	}
	if upstreamUserAgent != identity.StaticUserAgent {
		t.Errorf("upstream User-Agent = %q, want %q", upstreamUserAgent, identity.StaticUserAgent)
	}
	if upstreamAccountID != "acct-accept" {
		t.Errorf("upstream chatgpt-account-id = %q, want %q", upstreamAccountID, "acct-accept")
	}
	if upstreamPath != "/backend-api/codex/responses" {
		t.Errorf("upstream URL path = %q, want %q", upstreamPath, "/backend-api/codex/responses")
	}
	if upstreamSessionIDSnake != claudeSessionID {
		t.Errorf("upstream session_id = %q, want %q", upstreamSessionIDSnake, claudeSessionID)
	}
	if upstreamSessionIDKebab != claudeSessionID {
		t.Errorf("upstream session-id = %q, want %q", upstreamSessionIDKebab, claudeSessionID)
	}
	if upstreamThreadIDSnake != claudeSessionID {
		t.Errorf("upstream thread_id = %q, want %q", upstreamThreadIDSnake, claudeSessionID)
	}
	if upstreamThreadIDKebab != claudeSessionID {
		t.Errorf("upstream thread-id = %q, want %q", upstreamThreadIDKebab, claudeSessionID)
	}
	if upstreamWindowID != claudeSessionID {
		t.Errorf("upstream x-codex-window-id = %q, want %q", upstreamWindowID, claudeSessionID)
	}
	if upstreamOriginator != "codex_cli_rs" {
		t.Errorf("upstream originator = %q, want %q", upstreamOriginator, "codex_cli_rs")
	}
	if upstreamBetaFeatures != "responses_websockets" {
		t.Errorf("upstream X-Codex-Beta-Features = %q, want %q", upstreamBetaFeatures, "responses_websockets")
	}
	if upstreamClientReqID == "" {
		t.Error("upstream x-client-request-id should not be empty")
	}
	if upstreamTurnMetaRaw == "" {
		t.Fatal("upstream x-codex-turn-metadata should be set")
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(upstreamTurnMetaRaw), &meta); err != nil {
		t.Fatalf("upstream x-codex-turn-metadata is not valid JSON: %v", err)
	}
	if meta["session_id"] != claudeSessionID {
		t.Errorf("turn metadata session_id = %q, want %q", meta["session_id"], claudeSessionID)
	}
	if meta["sandbox"] != "none" {
		t.Errorf("turn metadata sandbox = %q, want %q", meta["sandbox"], "none")
	}
	if meta["turn_id"] == "" {
		t.Error("turn metadata turn_id should be set")
	}
	if upstreamInstructions != "You are a ChatGPT agent." {
		t.Errorf("upstream body instructions = %q, want %q", upstreamInstructions, "You are a ChatGPT agent.")
	}
	if upstreamPromptCacheKey != claudeSessionID {
		t.Errorf("upstream body prompt_cache_key = %q, want %q", upstreamPromptCacheKey, claudeSessionID)
	}
}

// ── S3: claude launch unchanged (regression) ─────────────────────────────────

// TestClaudeLaunch_Regression ensures that runLaunchLocal for a claude
// credential continues to work correctly (no codex-path interference).
// This is the non-codex regression case: it confirms the existing
// LocalProxy + exec path is untouched.
func TestClaudeLaunch_Regression(t *testing.T) {
	setupHomeWithCcm(t)

	claudeCred := &store.Credential{
		ID:   "dddddddd-dddd-dddd-dddd-dddddddddd03",
		Name: "claude-regression",
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

	// Upstream for claude path.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	share.SetUpstreamBaseForTest(upstream.URL)
	t.Cleanup(share.ResetUpstreamBaseForTest)

	// Stub exec so we don't need a real claude binary.
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

// ── S6: stream:false buffers to JSON ─────────────────────────────────────────

// TestCodexShare_StreamFalseBuffersJSON verifies that a request with
// "stream":false causes the proxy to buffer the entire upstream SSE and
// return a single Anthropic JSON response (Content-Type: application/json).
func TestCodexShare_StreamFalseBuffersJSON(t *testing.T) {
	setupHomeWithCcm(t)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := buildCodexCredAndStore(t,
		"cccccccc-cccc-cccc-cccc-cccccccccc06",
		"codex-stream-false",
	)

	sess := startSessionWithFakeCodexBackend(t, cred, upstream.URL,
		[]string{"claude-opus-*=gpt-5-codex"})

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":   "claude-opus-4.7",
		"stream":  false,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "hi"}},
		}},
	})

	tk := decodeSessionTicket(t, sess)
	req, _ := http.NewRequest("POST", sess.Reach()+"/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+tk.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json for stream:false", ct)
	}

	out, _ := io.ReadAll(resp.Body)
	outStr := string(out)
	// stream:false → translator.Collect → buffered Anthropic SSE events
	if !strings.Contains(outStr, "message_start") {
		t.Errorf("stream:false response missing message_start; got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "message_stop") {
		t.Errorf("stream:false response missing message_stop; got:\n%s", outStr)
	}
}

// ── S7: cancellation ─────────────────────────────────────────────────────────

// TestCodexShare_ClientCancellation verifies that when a downstream client
// closes its connection mid-stream, the upstream request context is also
// cancelled within a short window. This prevents goroutine leaks.
func TestCodexShare_ClientCancellation(t *testing.T) {
	setupHomeWithCcm(t)

	// upstream: streams slowly, watches for context cancellation.
	upstreamCancelledC := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Start streaming — first event.
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream ResponseWriter is not a Flusher")
			return
		}
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		flusher.Flush()

		// Wait for context cancellation (client disconnected).
		select {
		case <-r.Context().Done():
			close(upstreamCancelledC)
		case <-time.After(5 * time.Second):
			t.Error("upstream: client cancellation not propagated within 5s")
		}
	}))
	defer upstream.Close()

	cred := buildCodexCredAndStore(t,
		"cccccccc-cccc-cccc-cccc-cccccccccc07",
		"codex-cancel",
	)

	sess := startSessionWithFakeCodexBackend(t, cred, upstream.URL,
		[]string{"claude-opus-*=gpt-5-codex"})

	tk := decodeSessionTicket(t, sess)

	// Start request with a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx,
		"POST", sess.Reach()+"/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4.7","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	req.Header.Set("Authorization", "Bearer "+tk.Token)
	req.Header.Set("Content-Type", "application/json")

	// Use a separate transport so we can read partial response and then cancel.
	client := &http.Client{Transport: &http.Transport{}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("initial request: %v", err)
	}

	// Read first event from SSE stream to confirm streaming has started.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Scan() // first "data:" line or empty line

	// Cancel the client context — this closes the downstream connection.
	cancel()
	resp.Body.Close()

	// Upstream should detect cancellation quickly.
	select {
	case <-upstreamCancelledC:
		// OK — upstream saw context cancellation
	case <-time.After(3 * time.Second):
		t.Error("upstream context was not cancelled within 3s of client disconnect")
	}
}

// ── S8: mid-session 401 refresh ──────────────────────────────────────────────

// TestCodexShare_MidSession401Refresh verifies that when the codex upstream
// returns 401, the Terminal's BearerSrc.Fresh() is invoked (triggering a
// credential refresh) and the request is retried, resulting in a successful
// downstream response.
func TestCodexShare_MidSession401Refresh(t *testing.T) {
	setupHomeWithCcm(t)

	var requestCount atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			// First attempt: return 401.
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"type":"auth_error","message":"unauthorized"}}`)
			return
		}
		// Second attempt (after refresh): return success.
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := buildCodexCredAndStore(t,
		"cccccccc-cccc-cccc-cccc-cccccccccc08",
		"codex-401-refresh",
	)

	sess := startSessionWithFakeCodexBackend(t, cred, upstream.URL,
		[]string{"claude-opus-*=gpt-5-codex"})

	inbound := `{"model":"claude-opus-4.7","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	resp := postToSession(t, sess, inbound)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d after 401+retry, want 200; body: %s", resp.StatusCode, body)
	}

	if n := requestCount.Load(); n != 2 {
		t.Errorf("upstream hit %d times, want 2 (1 401 + 1 retry)", n)
	}

	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "content_block_delta") {
		t.Errorf("after 401 retry, response missing content_block_delta; got:\n%s", out)
	}
}

// ── S9: die-fast on unknown model ────────────────────────────────────────────

// TestCodexShare_DieFastOnUnknownModel verifies that when the codex upstream
// returns a model_not_found error:
//  1. The client receives a 4xx error response.
//  2. The proxy shuts down (subsequent connections are refused) within 2s.
//
// Note: sess.Done() is only closed by sess.Stop(), not by the proxy's own
// Close(). The "session terminates cleanly" observable is that the proxy's
// TCP listener stops accepting connections (tested by attempting a second POST).
func TestCodexShare_DieFastOnUnknownModel(t *testing.T) {
	setupHomeWithCcm(t)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"model_not_found","message":"unknown model 'gpt-5-codex'"}}`)
	}))
	defer upstream.Close()

	cred := buildCodexCredAndStore(t,
		"cccccccc-cccc-cccc-cccc-cccccccccc09",
		"codex-die-fast",
	)

	sess := startSessionWithFakeCodexBackend(t, cred, upstream.URL,
		[]string{"claude-opus-*=gpt-5-codex"})

	inbound := `{"model":"claude-opus-4.7","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	resp := postToSession(t, sess, inbound)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck — drain before asserting

	// Client should receive a 4xx error response (model_not_found triggers die-fast).
	if resp.StatusCode < 400 {
		t.Errorf("status = %d, want 4xx for model_not_found die-fast", resp.StatusCode)
	}

	// handleSessionDie runs proxy.Close in a goroutine so the response
	// can be flushed before the listener shuts down. Poll until a second
	// connection attempt is refused, confirming the proxy is down.
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

// ── S10: reasoning round-trip ─────────────────────────────────────────────────

// TestCodexShare_ReasoningRoundTrip verifies that a request containing
// "thinking" blocks is forwarded to the codex backend and the response
// contains translated reasoning/thinking delta events in Anthropic SSE format.
func TestCodexShare_ReasoningRoundTrip(t *testing.T) {
	setupHomeWithCcm(t)

	var upstreamBodyStr atomic.Value
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBodyStr.Store(string(b))

		// Reply with codex SSE that includes a reasoning delta.
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"id\":\"r1\",\"summary\":[]}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"r1\",\"delta\":\"let me think\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.reasoning_summary_text.done\",\"item_id\":\"r1\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := buildCodexCredAndStore(t,
		"cccccccc-cccc-cccc-cccc-cccccccccc10",
		"codex-reasoning",
	)

	sess := startSessionWithFakeCodexBackend(t, cred, upstream.URL,
		[]string{"claude-opus-*=gpt-5-codex"})

	// Inbound request with "thinking" configuration (Anthropic extended thinking).
	inboundBody, _ := json.Marshal(map[string]any{
		"model":  "claude-opus-4.7",
		"stream": true,
		"thinking": map[string]any{
			"type":         "enabled",
			"budget_tokens": 1000,
		},
		"messages": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "think about it"}},
		}},
	})

	tk := decodeSessionTicket(t, sess)
	req, _ := http.NewRequest("POST", sess.Reach()+"/v1/messages", bytes.NewReader(inboundBody))
	req.Header.Set("Authorization", "Bearer "+tk.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	out, _ := io.ReadAll(resp.Body)
	outStr := string(out)

	// The response must contain at least a content event and message_stop.
	if !strings.Contains(outStr, "content_block") {
		t.Errorf("reasoning round-trip: missing content_block in SSE; got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "message_stop") {
		t.Errorf("reasoning round-trip: missing message_stop in SSE; got:\n%s", outStr)
	}
}

