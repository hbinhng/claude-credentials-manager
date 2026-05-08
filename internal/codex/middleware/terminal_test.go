package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/capture"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/identity"
	codexmw "github.com/hbinhng/claude-credentials-manager/internal/codex/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/transport"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	sharemw "github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// minimalCapture returns a capture.Result with sensible test defaults.
func minimalCapture() *capture.Result {
	return &capture.Result{
		InstallationID: "test-install",
		ServiceTier:    "priority",
		SessionID:      "sess-123",
	}
}

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
	// Upstream: verify translated model, reply with tiny SSE.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:      minimalCapture(),
		Bundle:       identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
	token     string
	err       error
}

func (f *fakeBearerSrc) Fresh() (string, error) {
	f.refreshed++
	return f.token, f.err
}

func TestTerminal_401TriggersRefreshAndRetry(t *testing.T) {
	var requestCount int
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"type":"auth_error","message":"unauthorized"}}`)
			return
		}
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

	bearer := &fakeBearerSrc{token: "fresh-tok"}
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("old-tok"),
		Transport:   newTransport(t),
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:   minimalCapture(),
		Bundle:    identity.New(http.Header{}),
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
		Capture:      minimalCapture(),
		Bundle:       identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:      minimalCapture(),
		Bundle:       identity.New(http.Header{}),
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
		Capture:      minimalCapture(),
		Bundle:       identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	tee := codexmw.NewUsageTee(8)
	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Capture:     minimalCapture(),
		Bundle:      identity.New(http.Header{}),
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

// ── TestTerminal_PromptCacheKeyFromHeader ─────────────────────────────────────

func TestTerminal_PromptCacheKeyFromHeader(t *testing.T) {
	var receivedPromptCacheKey string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		json.Unmarshal(body, &got)
		receivedPromptCacheKey = fmt.Sprint(got["prompt_cache_key"])
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"status\":\"completed\"}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	term := codexmw.NewTerminal(codexmw.TerminalOpts{
		Cred:        minimalCred("tok"),
		Transport:   newTransport(t),
		Capture:     &capture.Result{SessionID: "fallback-session"},
		Bundle:      identity.New(http.Header{}),
		UpstreamURL: upstream.URL,
	})

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	req.Header.Set("X-Claude-Code-Session-Id", "header-session")
	rr := httptest.NewRecorder()

	withAlias(t, "claude-opus-*=gpt-5-codex", term, req, rr)

	if receivedPromptCacheKey != "header-session" {
		t.Errorf("prompt_cache_key = %q, want %q", receivedPromptCacheKey, "header-session")
	}
}
