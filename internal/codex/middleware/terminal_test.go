package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/identity"
	codexmw "github.com/hbinhng/claude-credentials-manager/internal/codex/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/transport"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	sharemw "github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// minimalCred builds a codex credential with the given access token.
func minimalCred(tok string) *store.Credential {
	return &store.Credential{
		Provider: "codex",
		Tokens:   &store.CodexTokens{AccessToken: tok, AccountID: "acct-1"},
	}
}

// newTransport creates a bogdanfinn transport that skips TLS verification.
func newTransport(t *testing.T) *transport.Transport {
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

// withAlias runs the given request through an AliasRewrite step and then
// calls the Terminal handler. This is how alias context values (which use
// unexported keys) get injected in black-box tests.
func withAlias(t *testing.T, aliasRule string, term *codexmw.Terminal, req *http.Request, rr *httptest.ResponseRecorder) {
	t.Helper()
	var m *alias.Map
	var err error
	if aliasRule != "" {
		m, err = alias.Parse([]string{aliasRule})
		if err != nil {
			t.Fatalf("alias.Parse: %v", err)
		}
	} else {
		m, _ = alias.Parse(nil)
	}
	ar := sharemw.NewAliasRewrite(m)
	ar.Apply(term).ServeHTTP(rr, req)
}

// ── TestTerminal_HappyPath ────────────────────────────────────────────────────

func TestTerminal_HappyPath(t *testing.T) {
	// Upstream: verify URL path, translated model, reply with tiny SSE.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pin the omniroute pivot URL change at the middleware layer:
		// outbound requests must hit /backend-api/codex/responses, not /v1/responses.
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("upstream URL path = %q, want %q", r.URL.Path, "/backend-api/codex/responses")
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("upstream: parse body: %v", err)
		}
		if got["model"] != "gpt-5-codex" {
			t.Errorf("upstream model = %v, want gpt-5-codex", got["model"])
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

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	out := rr.Body.String()
	if !strings.Contains(out, "content_block_delta") {
		t.Errorf("missing content_block_delta in output:\n%s", out)
	}
	if !strings.Contains(out, "message_stop") {
		t.Errorf("missing message_stop in output:\n%s", out)
	}
}

// ── TestTerminal_PropagatesClaudeSessionID ────────────────────────────────────

func TestTerminal_PropagatesClaudeSessionID(t *testing.T) {
	const claudeSessionID = "019e0a01-5569-7480-8945-f61f37958342"
	var (
		gotSessionIDSnake  string
		gotSessionIDKebab  string
		gotThreadIDSnake   string
		gotThreadIDKebab   string
		gotWindowID        string
		gotOriginator      string
		gotBetaFeatures    string
		gotTurnMetaRaw     string
		gotClientRequestID string
		gotInstructions    string
		gotPromptCacheKey  string
	)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionIDSnake = r.Header.Get("Session_id")
		gotSessionIDKebab = r.Header.Get("Session-Id")
		gotThreadIDSnake = r.Header.Get("Thread_id")
		gotThreadIDKebab = r.Header.Get("Thread-Id")
		gotWindowID = r.Header.Get("X-Codex-Window-Id")
		gotOriginator = r.Header.Get("Originator")
		gotBetaFeatures = r.Header.Get("X-Codex-Beta-Features")
		gotTurnMetaRaw = r.Header.Get("X-Codex-Turn-Metadata")
		gotClientRequestID = r.Header.Get("X-Client-Request-Id")
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err == nil {
			gotInstructions, _ = parsed["instructions"].(string)
			gotPromptCacheKey, _ = parsed["prompt_cache_key"].(string)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	req.Header.Set("X-Claude-Code-Session-Id", claudeSessionID)
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotSessionIDSnake != claudeSessionID {
		t.Errorf("upstream session_id = %q, want %q", gotSessionIDSnake, claudeSessionID)
	}
	if gotSessionIDKebab != claudeSessionID {
		t.Errorf("upstream session-id = %q, want %q", gotSessionIDKebab, claudeSessionID)
	}
	if gotThreadIDSnake != claudeSessionID {
		t.Errorf("upstream thread_id = %q, want %q", gotThreadIDSnake, claudeSessionID)
	}
	if gotThreadIDKebab != claudeSessionID {
		t.Errorf("upstream thread-id = %q, want %q", gotThreadIDKebab, claudeSessionID)
	}
	if gotWindowID != claudeSessionID {
		t.Errorf("upstream x-codex-window-id = %q, want %q", gotWindowID, claudeSessionID)
	}
	if gotOriginator != "codex_cli_rs" {
		t.Errorf("upstream originator = %q, want %q", gotOriginator, "codex_cli_rs")
	}
	if gotBetaFeatures != "responses_websockets" {
		t.Errorf("upstream X-Codex-Beta-Features = %q, want %q", gotBetaFeatures, "responses_websockets")
	}
	if gotClientRequestID == "" {
		t.Error("upstream x-client-request-id should not be empty")
	}
	if gotTurnMetaRaw == "" {
		t.Fatal("upstream x-codex-turn-metadata should be set")
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(gotTurnMetaRaw), &meta); err != nil {
		t.Fatalf("upstream x-codex-turn-metadata is not valid JSON: %v", err)
	}
	if meta["session_id"] != claudeSessionID {
		t.Errorf("turn metadata session_id = %q, want %q", meta["session_id"], claudeSessionID)
	}
	if meta["sandbox"] != "none" {
		t.Errorf("turn metadata sandbox = %q, want %q", meta["sandbox"], "none")
	}
	if meta["turn_id"] == "" {
		t.Error("turn metadata turn_id should not be empty")
	}
	if gotInstructions != "You are a ChatGPT agent." {
		t.Errorf("body instructions = %q, want %q", gotInstructions, "You are a ChatGPT agent.")
	}
	if gotPromptCacheKey != claudeSessionID {
		t.Errorf("body prompt_cache_key = %q, want %q", gotPromptCacheKey, claudeSessionID)
	}
}

// ── TestTerminal_NoSessionIDWhenInboundHeaderMissing ─────────────────────────

func TestTerminal_NoSessionIDWhenInboundHeaderMissing(t *testing.T) {
	var (
		gotSessionIDSnake  string
		gotSessionIDKebab  string
		gotThreadIDSnake   string
		gotThreadIDKebab   string
		gotWindowID        string
		gotTurnMetaRaw     string
		gotClientRequestID string
		gotPromptCacheKey  string
	)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionIDSnake = r.Header.Get("Session_id")
		gotSessionIDKebab = r.Header.Get("Session-Id")
		gotThreadIDSnake = r.Header.Get("Thread_id")
		gotThreadIDKebab = r.Header.Get("Thread-Id")
		gotWindowID = r.Header.Get("X-Codex-Window-Id")
		gotTurnMetaRaw = r.Header.Get("X-Codex-Turn-Metadata")
		gotClientRequestID = r.Header.Get("X-Client-Request-Id")
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err == nil {
			gotPromptCacheKey, _ = parsed["prompt_cache_key"].(string)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	// deliberately no X-Claude-Code-Session-Id header
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotSessionIDSnake != "" || gotSessionIDKebab != "" || gotThreadIDSnake != "" || gotThreadIDKebab != "" {
		t.Errorf("expected no session/thread headers when inbound X-Claude-Code-Session-Id is absent; got session=%q/%q thread=%q/%q",
			gotSessionIDSnake, gotSessionIDKebab, gotThreadIDSnake, gotThreadIDKebab)
	}
	if gotWindowID != "" {
		t.Errorf("x-codex-window-id should be absent when sessionID empty; got %q", gotWindowID)
	}
	if gotClientRequestID == "" {
		t.Error("x-client-request-id should still be set even without sessionID")
	}
	if gotTurnMetaRaw == "" {
		t.Fatal("x-codex-turn-metadata should still be set even without sessionID")
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(gotTurnMetaRaw), &meta); err != nil {
		t.Fatalf("x-codex-turn-metadata is not valid JSON: %v", err)
	}
	if _, has := meta["session_id"]; has {
		t.Errorf("turn metadata should NOT have session_id when sessionID empty; got %q", meta["session_id"])
	}
	if meta["turn_id"] == "" {
		t.Error("turn metadata turn_id should be set")
	}
	if gotPromptCacheKey != "" {
		t.Errorf("body prompt_cache_key should be absent when sessionID empty; got %q", gotPromptCacheKey)
	}
}

// ── TestTerminal_PassThrough ──────────────────────────────────────────────────

func TestTerminal_PassThrough(t *testing.T) {
	var receivedBody []byte
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Custom-Upstream", "yes")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "raw upstream bytes")
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	inboundBody := `{"model":"gpt-5-codex","input":[]}`
	body := bytes.NewBufferString(inboundBody)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	rr := httptest.NewRecorder()

	// No alias rule matches "gpt-5-codex" → passthrough.
	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if string(receivedBody) != inboundBody {
		t.Errorf("upstream received %q, want %q", receivedBody, inboundBody)
	}
	if got := rr.Body.String(); got != "raw upstream bytes" {
		t.Errorf("downstream body = %q, want %q", got, "raw upstream bytes")
	}
}

// ── TestTerminal_DieFastOnModelNotFound ──────────────────────────────────────

func TestTerminal_DieFastOnModelNotFound(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"model_not_found","message":"unknown model 'gpt-5-codex'"}}`)
	}))
	defer upstream.Close()

	var dieReason atomic.Value

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
		OnSessionDie: func(reason string) { dieReason.Store(reason) },
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	got := dieReason.Load()
	if got == nil {
		t.Fatal("OnSessionDie was not called")
	}
	if !strings.Contains(got.(string), "gpt-5-codex") {
		t.Errorf("die reason = %v, want to contain gpt-5-codex", got)
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── TestTerminal_DieFastOnInvalidRequestWithModel ─────────────────────────────

func TestTerminal_DieFastOnInvalidRequestWithModel(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// type==invalid_request_error, message contains model name
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"model gpt-5-codex is not available"}}`)
	}))
	defer upstream.Close()

	var called bool

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:         minimalCred("tok"),
		Transport:    newTransport(t),
		Bundle:       identity.New(minimalCred("tok")),
		UpstreamURL:  upstream.URL,
		OnSessionDie: func(reason string) { called = true },
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if !called {
		t.Error("OnSessionDie not called for invalid_request_error+model in message")
	}
}

// ── TestTerminal_StreamFalseBuffersCollected ──────────────────────────────────

func TestTerminal_StreamFalseBuffersCollected(t *testing.T) {
	// stream:false → Terminal uses StreamTranslator.Collect which buffers the
	// entire upstream SSE and returns the translated Anthropic SSE bytes.
	// The downstream gets a complete, buffered (non-streaming) response.
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

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	// stream:false in the inbound body triggers the Collect (buffered) path.
	bodyBytes, _ := json.Marshal(map[string]any{
		"model":    "claude-opus-4.7",
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}}},
		"stream":   false,
	})
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(bodyBytes))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	// StreamTranslator.Collect buffers the translated Anthropic SSE events.
	// The result contains Anthropic SSE events (message_start, content_block_delta…)
	// collected before being written to the response in one shot.
	out := rr.Body.String()
	if !strings.Contains(out, "message_start") {
		t.Errorf("expected message_start in collected output, got:\n%s", out)
	}
	// The full translation up to message_stop must be present.
	if !strings.Contains(out, "message_stop") {
		t.Errorf("expected message_stop in collected output, got:\n%s", out)
	}
	// Content-Type must be application/json (set by the stream:false path).
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// ── TestTerminal_401TriggersRefreshAndRetry ───────────────────────────────────

type fakeBearerSrc struct {
	refreshed int
	token     string // value to return AND, when cred is non-nil, write into cred.Tokens.AccessToken
	err       error
	cred      *store.Credential // optional: when non-nil, Fresh mutates cred.Tokens.AccessToken
}

func (f *fakeBearerSrc) Fresh() (string, error) {
	f.refreshed++
	if f.cred != nil && f.cred.Tokens != nil && f.err == nil {
		f.cred.Tokens.AccessToken = f.token
	}
	return f.token, f.err
}

func TestTerminal_401TriggersRefreshAndRetry(t *testing.T) {
	var requestCount int
	var firstAuth, secondAuth string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			firstAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"type":"auth_error","message":"unauthorized"}}`)
			return
		}
		secondAuth = r.Header.Get("Authorization")
		// Second attempt succeeds.
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	// Single shared cred, mirroring production wiring (proxy passes the
	// same *store.Credential into Cred and Bundle in
	// terminalForProvider).
	cred := minimalCred("old-tok")
	bearer := &fakeBearerSrc{token: "fresh-tok", cred: cred}
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
		BearerSrc:   bearer,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if bearer.refreshed != 1 {
		t.Errorf("BearerSrc.Fresh() called %d times, want 1", bearer.refreshed)
	}
	if requestCount != 2 {
		t.Errorf("upstream received %d requests, want 2", requestCount)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if firstAuth != "Bearer old-tok" {
		t.Errorf("first attempt Authorization = %q, want %q", firstAuth, "Bearer old-tok")
	}
	if secondAuth != "Bearer fresh-tok" {
		t.Errorf("second attempt Authorization = %q, want %q (token must rotate after Fresh)", secondAuth, "Bearer fresh-tok")
	}
}

// ── TestTerminal_401RefreshFailureReturns502 ──────────────────────────────────

func TestTerminal_401RefreshFailureReturns502(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"type":"auth_error","message":"unauthorized"}}`)
	}))
	defer upstream.Close()

	bearer := &fakeBearerSrc{err: errors.New("refresh-failed")}
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
		BearerSrc:   bearer,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

// ── TestTerminal_TranslatorError ──────────────────────────────────────────────

func TestTerminal_TranslatorError(t *testing.T) {
	// Upstream should never be reached because the body is invalid.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be reached on translator error")
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	// AliasRewrite will parse the model out of the JSON. The body needs to
	// be valid JSON for AliasRewrite to pass, but TranslateRequest needs it
	// to have a valid Anthropic structure. We send a valid JSON that causes
	// a translator error by providing an invalid role in messages.
	badBody := `{"model":"claude-opus-4.7","messages":[{"role":"unknown","content":[]}]}`
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(badBody))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── TestTerminal_UpstreamError ────────────────────────────────────────────────

func TestTerminal_UpstreamError(t *testing.T) {
	// Immediately close to simulate connection error.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Close the connection forcibly.
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

// ── TestTerminal_PostMatched_StreamingPipeError ───────────────────────────────

func TestTerminal_PostMatched_StreamingPipeError(t *testing.T) {
	// Upstream sends a truncated SSE stream (no [DONE]). The scanner sees EOF
	// which is treated as clean termination by the translator (scanner.Err()
	// returns nil on EOF). So this tests the clean-early-close path.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Return without [DONE] → EOF → Pipe returns nil (clean close).
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	// Clean early-close: no panic, status 200, partial SSE output.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (partial stream is still 200)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "message_start") {
		t.Errorf("expected partial SSE in output:\n%s", rr.Body.String())
	}
}

// ── TestTerminal_DefaultUpstreamURL ──────────────────────────────────────────

func TestTerminal_DefaultUpstreamURL(t *testing.T) {
	// Verify that NewTerminal fills in the default URL.
	// We can't easily test the URL itself without a real network call,
	// but we verify the struct is created correctly.
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:      minimalCred("tok"),
		Transport: newTransport(t),
		Bundle:    identity.New(minimalCred("tok")),
		// UpstreamURL intentionally omitted → defaults to "https://chatgpt.com"
	})
	if term == nil {
		t.Fatal("NewTerminal returned nil")
	}
}

// ── TestTerminal_NoAliasMatch_NoSessionDie ────────────────────────────────────

func TestTerminal_NoAliasMatch_NoSessionDie(t *testing.T) {
	// model_not_found but no alias match → die-fast uses originalModel
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"model_not_found","message":"unknown model"}}`)
	}))
	defer upstream.Close()

	var dieReason atomic.Value
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:         minimalCred("tok"),
		Transport:    newTransport(t),
		Bundle:       identity.New(minimalCred("tok")),
		UpstreamURL:  upstream.URL,
		OnSessionDie: func(reason string) { dieReason.Store(reason) },
	})

	// Model doesn't match alias rule → passthrough path.
	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"gpt-5-codex","messages":[]}`))
	rr := httptest.NewRecorder()

	// "no-match" means the model gpt-5-codex doesn't match "claude-opus-*" pattern.
	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	// Die-fast still fires on model_not_found even in passthrough.
	if dieReason.Load() == nil {
		t.Fatal("OnSessionDie not called despite model_not_found")
	}
}

// ── TestTerminal_BodyReadError ────────────────────────────────────────────────

// errReader is an io.ReadCloser that returns an error on the first read.
type errReader struct{}

func (e errReader) Read(_ []byte) (int, error) { return 0, errors.New("read error") }
func (e errReader) Close() error               { return nil }

func TestTerminal_BodyReadError(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be reached when body read fails")
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Body = errReader{}
	rr := httptest.NewRecorder()

	// Run through alias middleware with a matching rule.
	// AliasRewrite reads the body first and will hit the error too.
	// Actually AliasRewrite reads body, but if Body is errReader it will fail at AliasRewrite.
	// We need to bypass AliasRewrite and call Terminal directly. However context keys are unexported.
	// Instead, set r.ContentLength=0 so AliasRewrite skips body reading, then swap body for terminal.
	// The simpler approach: set body to nil (ContentLength=0), AliasRewrite skips,
	// then inject the errReader via a wrapper.
	// Actually: AliasRewrite skips when r.Body == nil || r.ContentLength == 0.
	// We can call withAlias to skip rewrite and call term directly with errReader body.
	// But withAlias goes through AliasRewrite which will be a no-match since body is nil.

	// Strategy: use a zero-length body to bypass AliasRewrite, then replace body with errReader.
	// AliasRewrite reads body when ContentLength != 0.
	// If we pass no body, AliasRewrite passes through and Terminal reads r.Body directly.
	// But Terminal then gets an empty body, not an error.

	// Cleanest solution: test Terminal.ServeHTTP directly without going through withAlias.
	// The context won't have alias keys (AliasMatched=false, originalModel="", effectiveModel="").
	// That's OK: it'll hit the passthrough path, read body, get error, return 400.

	req2 := httptest.NewRequest("POST", "/v1/messages", nil)
	req2.Body = errReader{}
	rr2 := httptest.NewRecorder()
	term.ServeHTTP(rr2, req2) // no alias context → passthrough path → read body → error

	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on body read error", rr2.Code)
	}
	_ = rr
	_ = req
}

// ── TestTerminal_CollectContextCancel ─────────────────────────────────────────

func TestTerminal_CollectContextCancel(t *testing.T) {
	// Trigger Collect error by cancelling context mid-stream.
	// The upstream sends a slow stream; we cancel the context after
	// the response headers arrive but before the body is fully read.
	requestReceived := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(requestReceived) // signal that headers were sent
		// Block until the client context is cancelled (simulates slow upstream).
		<-r.Context().Done()
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":    "claude-opus-4.7",
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}}},
		"stream":   false,
	})
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(bodyBytes))
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)
	}()

	// Wait for upstream to receive the request, then cancel.
	select {
	case <-requestReceived:
		cancel()
	case <-done:
		// Handler finished before we could cancel; accept as a pass.
		cancel()
		return
	}
	<-done

	// Context cancellation causes either Transport.Do to fail (502) or
	// Collect to return ctx.Err() (500). Either is ≥400.
	if rr.Code < 400 {
		t.Errorf("status = %d, want ≥400 on context cancellation", rr.Code)
	}
}

// ── TestTerminal_401NoBearerSrcReturns401 ────────────────────────────────────

func TestTerminal_401NoBearerSrcReturns401(t *testing.T) {
	// 401 from upstream + nil BearerSrc → return the 401 unchanged.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"type":"auth_error","message":"bad creds"}}`)
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
		// BearerSrc intentionally nil → early return on 401
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	// 401 is a non-2xx → die-fast check, then writeAnthropicError with 401.
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ── TestTerminal_ShouldDieFast_MalformedJSON ──────────────────────────────────

func TestTerminal_ShouldDieFast_MalformedJSON(t *testing.T) {
	// Upstream returns non-JSON error body → shouldDieFast returns false → no die
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `not json at all`)
	}))
	defer upstream.Close()

	var called bool
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:         minimalCred("tok"),
		Transport:    newTransport(t),
		Bundle:       identity.New(minimalCred("tok")),
		UpstreamURL:  upstream.URL,
		OnSessionDie: func(reason string) { called = true },
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if called {
		t.Error("OnSessionDie should not be called when error body is malformed JSON")
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── TestTerminal_ShouldDieFast_InvalidRequestNoModelMatch ─────────────────────

func TestTerminal_ShouldDieFast_InvalidRequestNoModelMatch(t *testing.T) {
	// invalid_request_error but message does not contain the model name → no die
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"some other problem"}}`)
	}))
	defer upstream.Close()

	var called bool
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:         minimalCred("tok"),
		Transport:    newTransport(t),
		Bundle:       identity.New(minimalCred("tok")),
		UpstreamURL:  upstream.URL,
		OnSessionDie: func(reason string) { called = true },
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if called {
		t.Error("OnSessionDie should not be called when message does not contain model name")
	}
}

// ── TestTerminal_StreamTrue ───────────────────────────────────────────────────

func TestTerminal_StreamTrue(t *testing.T) {
	// stream:true explicitly set → Pipe path (not Collect).
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":    "claude-opus-4.7",
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}}},
		"stream":   true, // explicit stream:true → Pipe, not Collect
	})
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(bodyBytes))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(rr.Body.String(), "content_block_delta") {
		t.Errorf("expected content_block_delta in streaming output:\n%s", rr.Body.String())
	}
}

// ── TestTerminal_PipeErrorEmitsErrorEvent ────────────────────────────────────

func TestTerminal_PipeErrorEmitsErrorEvent(t *testing.T) {
	// Trigger a scanner-level I/O error in Pipe() without context cancellation.
	// We send a single SSE data line that is larger than the scanner's 4 MiB token
	// buffer, causing bufio.Scanner.Err() to return bufio.ErrTooLong (not nil and
	// not context.Canceled).
	const scannerMaxTokenSize = 4 * 1024 * 1024 // must match stream.go's scanner buffer

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Write a data line larger than scannerMaxTokenSize.
		// bufio.Scanner will return ErrTooLong when it can't fit the token.
		oversize := make([]byte, scannerMaxTokenSize+1)
		for i := range oversize {
			oversize[i] = 'x'
		}
		io.WriteString(w, "data: ")
		w.Write(oversize)
		io.WriteString(w, "\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	// The Pipe error path emits a best-effort SSE error event. The response
	// already had its headers/status sent (200 SSE), so we verify the error
	// event appears in the output.
	out := rr.Body.String()
	if !strings.Contains(out, "event: error") {
		t.Errorf("expected error SSE event in output after scanner overflow, got:\n%s", out[:min(len(out), 500)])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── TestTerminal_QuotaCacheWired ─────────────────────────────────────────────

// fakeUsageCacheT is a test-local UsageCache implementation.
type fakeUsageCacheT struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeUsageCacheT) Update(_ string, _ float64, _ time.Time) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
}

func TestTerminal_QuotaCacheWired(t *testing.T) {
	// Upstream returns codex quota headers on 2xx.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.Header().Set("x-codex-5h-used", "0.25")
		w.Header().Set("x-codex-5h-resets-at", reset)
		w.Header().Set("x-codex-7d-used", "0.50")
		w.Header().Set("x-codex-7d-resets-at", reset)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cache := &fakeUsageCacheT{}
	qc := codexmw.NewQuotaCache(cache)

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
		QuotaCache:  qc,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	cache.mu.Lock()
	got := cache.calls
	cache.mu.Unlock()
	if got != 2 {
		t.Errorf("QuotaCache.Update called %d times, want 2 (5h + 7d)", got)
	}
}

// ── TestTerminal_UsageTeeWired ────────────────────────────────────────────────

func TestTerminal_UsageTeeWired(t *testing.T) {
	// Upstream sends a complete SSE stream with usage.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"response\":{\"id\":\"resp_t1\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	tee := codexmw.NewUsageTee(8)
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Bundle:      identity.New(minimalCred("tok")),
		UpstreamURL: upstream.URL,
		UsageTee:    tee,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	snap := tee.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("tee snapshot len = %d, want 1", len(snap))
	}
	if snap[0].InputTokens != 7 || snap[0].OutputTokens != 3 {
		t.Errorf("usage event = %+v, want InputTokens=7 OutputTokens=3", snap[0])
	}
}

// ── TestApplyDynamicCodexHeaders_PopulatesAllFromSession ─────────────────────

func TestApplyDynamicCodexHeaders_PopulatesAllFromSession(t *testing.T) {
	const sessionID = "019e0a01-5569-7480-8945-f61f37958342"
	req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header = http.Header{}

	codexmw.ApplyDynamicCodexHeadersForTest(req, sessionID)

	checks := map[string]string{
		"Session_id":        sessionID,
		"Session-Id":        sessionID,
		"Thread_id":         sessionID,
		"Thread-Id":         sessionID,
		"X-Codex-Window-Id": sessionID,
	}
	for key, want := range checks {
		if got := req.Header.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if got := req.Header.Get("X-Client-Request-Id"); got == "" {
		t.Error("X-Client-Request-Id should be set; got empty")
	}

	turnMetaRaw := req.Header.Get("X-Codex-Turn-Metadata")
	if turnMetaRaw == "" {
		t.Fatal("X-Codex-Turn-Metadata should be set; got empty")
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(turnMetaRaw), &meta); err != nil {
		t.Fatalf("X-Codex-Turn-Metadata is not valid JSON: %v (raw=%q)", err, turnMetaRaw)
	}
	if meta["turn_id"] == "" {
		t.Errorf("turn_id should be a non-empty UUID; got %q", meta["turn_id"])
	}
	if meta["sandbox"] != "none" {
		t.Errorf("sandbox = %q, want %q", meta["sandbox"], "none")
	}
	if meta["session_id"] != sessionID {
		t.Errorf("session_id (in turn metadata) = %q, want %q", meta["session_id"], sessionID)
	}
}

// ── TestApplyDynamicCodexHeaders_NoSessionID ─────────────────────────────────

func TestApplyDynamicCodexHeaders_NoSessionID(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header = http.Header{}

	codexmw.ApplyDynamicCodexHeadersForTest(req, "")

	for _, key := range []string{"Session_id", "Session-Id", "Thread_id", "Thread-Id", "X-Codex-Window-Id"} {
		if got := req.Header.Get(key); got != "" {
			t.Errorf("%s should be absent when sessionID is empty; got %q", key, got)
		}
	}
	if got := req.Header.Get("X-Client-Request-Id"); got == "" {
		t.Error("X-Client-Request-Id should still be set even without sessionID; got empty")
	}
	turnMetaRaw := req.Header.Get("X-Codex-Turn-Metadata")
	if turnMetaRaw == "" {
		t.Fatal("X-Codex-Turn-Metadata should still be set without sessionID")
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(turnMetaRaw), &meta); err != nil {
		t.Fatalf("X-Codex-Turn-Metadata is not valid JSON: %v", err)
	}
	if meta["turn_id"] == "" {
		t.Errorf("turn_id should be set; got %q", meta["turn_id"])
	}
	if _, has := meta["session_id"]; has {
		t.Errorf("session_id should NOT be in turn metadata when sessionID is empty; got %q", meta["session_id"])
	}
}

// ── TestApplyDynamicCodexHeaders_FreshTurnIDPerCall ──────────────────────────

func TestApplyDynamicCodexHeaders_FreshTurnIDPerCall(t *testing.T) {
	const sessionID = "019e0a01-5569-7480-8945-f61f37958342"
	req1, _ := http.NewRequest("POST", "https://chatgpt.com", nil)
	req1.Header = http.Header{}
	req2, _ := http.NewRequest("POST", "https://chatgpt.com", nil)
	req2.Header = http.Header{}

	codexmw.ApplyDynamicCodexHeadersForTest(req1, sessionID)
	codexmw.ApplyDynamicCodexHeadersForTest(req2, sessionID)

	if req1.Header.Get("X-Client-Request-Id") == req2.Header.Get("X-Client-Request-Id") {
		t.Error("X-Client-Request-Id should differ between calls")
	}

	parseTurnID := func(raw string) string {
		var m map[string]string
		_ = json.Unmarshal([]byte(raw), &m)
		return m["turn_id"]
	}
	if parseTurnID(req1.Header.Get("X-Codex-Turn-Metadata")) == parseTurnID(req2.Header.Get("X-Codex-Turn-Metadata")) {
		t.Error("turn_id should differ between calls")
	}
}

// orderedRecorder records the relative order of WriteHeader vs Write
// calls on a ResponseWriter. Used to verify pre-flush behavior in
// streaming-sensitive paths.
type orderedRecorder struct {
	*httptest.ResponseRecorder
	headerWrittenAt int // sequence number when WriteHeader fired (-1 = never)
	firstWriteAt    int // sequence number of the first Write call (-1 = never)
	seq             int
}

func newOrderedRecorder() *orderedRecorder {
	return &orderedRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		headerWrittenAt:  -1,
		firstWriteAt:     -1,
	}
}

func (o *orderedRecorder) WriteHeader(code int) {
	o.seq++
	o.headerWrittenAt = o.seq
	o.ResponseRecorder.WriteHeader(code)
}

func (o *orderedRecorder) Write(b []byte) (int, error) {
	o.seq++
	if o.firstWriteAt == -1 {
		o.firstWriteAt = o.seq
	}
	return o.ResponseRecorder.Write(b)
}

func (o *orderedRecorder) Flush() {
	o.ResponseRecorder.Flush()
}

// TestTerminal_StreamFalsePreFlushesHeaders ensures the stream:false
// path writes response headers BEFORE Collect blocks, so Cloudflare's
// "no first byte" timeout is satisfied even for long-running
// generations. Per Cloudflare Quick Tunnel behavior the tunnel returns
// 524 if no bytes (headers count) reach the edge within ~100s.
func TestTerminal_StreamFalsePreFlushesHeaders(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
	})

	// stream:false body
	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := newOrderedRecorder()

	// Inline alias-rewrite + ServeHTTP (mirrors withAlias, which is
	// typed for *httptest.ResponseRecorder and cannot accept the
	// orderedRecorder wrapper).
	m, err := alias.Parse([]string{"claude-opus-*=gpt-5-codex"})
	if err != nil {
		t.Fatalf("alias.Parse: %v", err)
	}
	sharemw.NewAliasRewrite(m).Apply(term).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.headerWrittenAt < 0 {
		t.Fatal("WriteHeader was never called")
	}
	if rr.firstWriteAt < 0 {
		t.Fatal("Write was never called")
	}
	if rr.headerWrittenAt > rr.firstWriteAt {
		t.Errorf("WriteHeader fired AFTER first Write (header seq=%d, first write seq=%d) — pre-flush ordering is broken",
			rr.headerWrittenAt, rr.firstWriteAt)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	// StreamTranslator.Collect emits Anthropic SSE event bytes (matching
	// the existing TestTerminal_StreamFalseBuffersCollected contract):
	// the body must include both message_start and message_stop markers
	// so we know Collect ran to completion AFTER headers flushed.
	out := rr.Body.String()
	if !strings.Contains(out, "message_start") {
		t.Errorf("body missing message_start, got:\n%s", out)
	}
	if !strings.Contains(out, "message_stop") {
		t.Errorf("body missing message_stop, got:\n%s", out)
	}
}

// TestTerminal_StreamFalseCollectFailsAfterFlush exercises the
// post-flush error branch: headers are already on the wire (status
// 200, application/json), then Collect returns an error because
// upstream closed the connection mid-stream. The handler must NOT
// call WriteHeader again (which would log a Go warning); instead it
// emits an inline Anthropic-shaped error JSON as the body.
func TestTerminal_StreamFalseCollectFailsAfterFlush(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Send 200 + SSE content-type so middleware enters the SSE
		// path. Hijack and close to make scanner.Err() return an
		// unexpected EOF inside Collect.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Write a partial event then forcibly close the underlying
		// connection — this surfaces as a read error to Collect.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("upstream ResponseWriter does not support hijack")
			return
		}
		conn, bw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = bw.WriteString("data: {\"type\":\"response.created\"}\n") // no trailing \n\n + close
		_ = bw.Flush()
		_ = conn.Close()
	}))
	defer upstream.Close()

	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()

	m, err := alias.Parse([]string{"claude-opus-*=gpt-5-codex"})
	if err != nil {
		t.Fatalf("alias.Parse: %v", err)
	}
	sharemw.NewAliasRewrite(m).Apply(term).ServeHTTP(rr, req)

	// Headers were already flushed before Collect failed → status MUST
	// be 200 (we cannot WriteHeader twice) and Content-Type stays JSON.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (pre-flush already committed)", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	// Body must be a parseable Anthropic-shaped error object.
	var parsed map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v (body=%q)", err, rr.Body.String())
	}
	if got := parsed["type"]; got != "error" {
		t.Errorf("body type = %v, want %q", got, "error")
	}
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatalf("body.error is not an object: %v", parsed["error"])
	}
	if got := errObj["type"]; got != "api_error" {
		t.Errorf("body.error.type = %v, want %q", got, "api_error")
	}
	if _, hasMsg := errObj["message"]; !hasMsg {
		t.Errorf("body.error.message missing: %v", errObj)
	}
}

// TestTerminal_Overflow_Returns400 verifies that when chatgpt.com
// emits response.incomplete{max_output_tokens} without any actionable
// content delta, the terminal translates this into an Anthropic-shape
// HTTP 400 prompt-too-long error that Claude Code's reactive-compact
// path recognizes, rather than streaming a misleading
// stop_reason=max_tokens SSE response.
func TestTerminal_Overflow_Returns400(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"r1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs1\",\"summary\":[]}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs1\",\"summary\":[]}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r1\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":271392,\"output_tokens\":160}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if rr.Header().Get("Content-Type") == "text/event-stream" {
		t.Errorf("Content-Type must NOT be text/event-stream on overflow")
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not JSON: %v\nbody: %s", err, rr.Body.String())
	}
	errObj, _ := got["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("response missing error object: %s", rr.Body.String())
	}
	if errObj["type"] != "invalid_request_error" {
		t.Errorf("error.type = %v, want invalid_request_error", errObj["type"])
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "prompt is too long: 271392 tokens > 271552 maximum") {
		t.Errorf("error.message = %q, want substring \"prompt is too long: 271392 tokens > 271552 maximum\"", msg)
	}
}

// TestTerminal_NormalStream_Unaffected confirms the happy path is
// byte-identical to the pre-fix behavior: the classifier sees an
// actionable delta, returns non-overflow, and Pipe streams everything
// through MultiReader(replay, remaining) transparently.
func TestTerminal_NormalStream_Unaffected(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.done\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	out := rr.Body.String()
	for _, want := range []string{"message_start", "content_block_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in SSE output:\n%s", want, out)
		}
	}
	// Round-trip sanity: the actionable delta value made it through.
	if !strings.Contains(out, "answer") {
		t.Errorf("delta text \"answer\" missing from SSE output:\n%s", out)
	}
}

// TestTerminal_SummaryOnlyOverflow_Returns400 covers the trace-61f0
// shape: many reasoning_summary_text deltas followed by
// response.incomplete{max_output_tokens}. Per the spec, summary deltas
// are NOT actionable, so this must flip to overflow → 400.
func TestTerminal_SummaryOnlyOverflow_Returns400(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs1\",\"summary\":[]}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Planning...\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\" more thinking\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r1\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":271392,\"output_tokens\":160}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (summary-only output is still no actionable content)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "prompt is too long") {
		t.Errorf("response body missing prompt-too-long message:\n%s", rr.Body.String())
	}
}

// TestTerminal_UsageTee_NotFiredOnOverflow confirms that when the
// terminal short-circuits to 400, no usage event is recorded — there
// was no successful translated turn to account for.
func TestTerminal_UsageTee_NotFiredOnOverflow(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r1\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":271392,\"output_tokens\":160}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	tee := codexmw.NewUsageTee(8)
	cred := minimalCred("tok")
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        cred,
		Transport:   newTransport(t),
		Bundle:      identity.New(cred),
		UpstreamURL: upstream.URL,
		UsageTee:    tee,
	})

	body := bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", body)
	rr := httptest.NewRecorder()
	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	snap := tee.Snapshot()
	if len(snap) != 0 {
		t.Errorf("UsageTee recorded %d events on overflow, want 0", len(snap))
	}
}

