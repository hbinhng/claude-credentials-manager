package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	sharemw "github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
)

// fakeBearer is a stub sharemw.BearerSource.
type fakeBearer struct {
	tok string
	err error
}

func (f fakeBearer) Fresh() (string, error) { return f.tok, f.err }

// withAlias runs req through a real AliasRewrite step (built from aliasRule,
// or an empty map when aliasRule is "") before invoking term. This is the
// only way to populate the alias/model context values the terminal reads —
// the keys in internal/share/middleware/context.go are unexported and set
// solely by AliasRewrite.Apply. Mirrors internal/codex/middleware/terminal_test.go's
// withAlias helper.
func withAlias(t *testing.T, aliasRule string, term *Terminal, req *http.Request, rr http.ResponseWriter) {
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

// ── TestTerminal_RewritesModelOnMatch ────────────────────────────────────────

func TestTerminal_RewritesModelOnMatch(t *testing.T) {
	var gotModel, gotAuth, gotUA, gotCT string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		gotModel, _ = m["model"].(string)
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "tok"}})
	body := `{"model":"claude-sonnet","stream":false}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-sonnet=grok-4.5", term, req, rr)

	if gotModel != "grok-4.5" {
		t.Fatalf("upstream model = %q, want grok-4.5", gotModel)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want %q", gotAuth, "Bearer tok")
	}
	if gotUA == "" {
		t.Error("User-Agent header not set")
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

// ── TestTerminal_RewriteIsOrderPreserving ────────────────────────────────────

func TestTerminal_RewriteIsOrderPreserving(t *testing.T) {
	// Verify the model rewrite is a textual splice, not a JSON
	// unmarshal/remarshal — key order (and unrelated formatting) must
	// survive so prompt-cache prefixes stay stable.
	var gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "tok"}})
	// zebra/alpha ordering deliberately not alphabetical; a marshal-based
	// rewrite via map[string]json.RawMessage would sort or reorder keys.
	body := `{"zebra":1,"model":"claude-sonnet","alpha":2}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-sonnet=grok-4.5", term, req, rr)

	want := `{"zebra":1,"model":"grok-4.5","alpha":2}`
	if gotBody != want {
		t.Fatalf("upstream body = %q, want %q (order not preserved)", gotBody, want)
	}
}

// ── TestTerminal_DefaultModelWhenUnmatched ───────────────────────────────────

func TestTerminal_DefaultModelWhenUnmatched(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		gotModel, _ = m["model"].(string)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus"}`))
	rr := httptest.NewRecorder()

	// "claude-sonnet=grok-4.5" does not match "claude-opus" → unmatched path.
	withAlias(t, "claude-sonnet=grok-4.5", term, req, rr)

	if gotModel != "grok-composer-2.5-fast" {
		t.Fatalf("upstream model = %q, want default", gotModel)
	}
}

func TestTerminal_DefaultModelIsConfigurable(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		gotModel, _ = m["model"].(string)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}, DefaultModel: "grok-custom"})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus"}`))
	rr := httptest.NewRecorder()

	withAlias(t, "claude-sonnet=grok-4.5", term, req, rr)

	if gotModel != "grok-custom" {
		t.Fatalf("upstream model = %q, want grok-custom", gotModel)
	}
}

// ── TestTerminal_StreamingSSERelay ───────────────────────────────────────────

// flushRecorder wraps httptest.ResponseRecorder to also implement
// http.Flusher, so we can assert the terminal's flush-per-write path runs.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int32
}

func (f *flushRecorder) Flush() {
	atomic.AddInt32(&f.flushes, 1)
}

func TestTerminal_StreamingSSERelay(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "event: message_stop\ndata: {}\n\n")
		flusher.Flush()
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x","stream":true}`))
	rr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	out := rr.Body.String()
	if !strings.Contains(out, "message_start") || !strings.Contains(out, "message_stop") {
		t.Errorf("missing SSE events in output: %s", out)
	}
	if atomic.LoadInt32(&rr.flushes) == 0 {
		t.Error("expected Flush to be called on the flushing relay path")
	}
}

// ── TestTerminal_NonStreamJSONRelay ──────────────────────────────────────────

func TestTerminal_NonStreamJSONRelay(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Marker", "yes")
		_, _ = w.Write([]byte(`{"type":"message","id":"msg_1"}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x","stream":false}`))
	rr := httptest.NewRecorder()

	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Header().Get("X-Upstream-Marker") != "yes" {
		t.Error("upstream header not relayed")
	}
	if rr.Body.String() != `{"type":"message","id":"msg_1"}` {
		t.Errorf("body = %q", rr.Body.String())
	}
}

// ── TestTerminal_401RefreshRetry ─────────────────────────────────────────────

func TestTerminal_401RefreshRetry(t *testing.T) {
	var calls int
	var firstAuth, secondAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			firstAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		secondAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()

	bearer := &countingBearer{tok: "stale"}
	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: bearer})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()

	withAlias(t, "", term, req, rr)

	if calls != 2 {
		t.Fatalf("upstream called %d times, want 2", calls)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if firstAuth != "Bearer stale" {
		t.Errorf("first auth = %q", firstAuth)
	}
	if secondAuth != "Bearer fresh-1" {
		t.Errorf("second auth = %q, want rotated token", secondAuth)
	}
	if bearer.calls != 2 {
		t.Errorf("Fresh() called %d times, want 2 (initial + retry)", bearer.calls)
	}
}

// countingBearer returns a token that changes on every call, so tests can
// assert the retry path actually re-fetched a fresh token.
type countingBearer struct {
	tok   string
	calls int
}

func (c *countingBearer) Fresh() (string, error) {
	c.calls++
	if c.calls == 1 {
		return c.tok, nil
	}
	return "fresh-1", nil
}

// ── TestTerminal_DieFastOnModelNotFound ──────────────────────────────────────

func TestTerminal_DieFastOnModelNotFound(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"model_not_found","message":"nope"}}`))
	}))
	defer up.Close()

	var died atomic.Value
	term := NewTerminal(TerminalOpts{
		UpstreamURL:  up.URL,
		BearerSrc:    fakeBearer{tok: "t"},
		OnSessionDie: func(reason string) { died.Store(reason) },
	})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()

	withAlias(t, "x=grok-4.5", term, req, rr)

	got := died.Load()
	if got == nil {
		t.Fatal("OnSessionDie not called")
	}
	if !strings.Contains(got.(string), "grok-4.5") {
		t.Errorf("die reason = %v, want to mention target model", got)
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	var errResp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if errResp["type"] != "error" {
		t.Errorf("response type = %v, want error", errResp["type"])
	}
}

// ── TestTerminal_Upstream5xxReturnsAnthropicError ────────────────────────────

func TestTerminal_Upstream5xxReturnsAnthropicError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"boom"}}`))
	}))
	defer up.Close()

	var died bool
	term := NewTerminal(TerminalOpts{
		UpstreamURL:  up.URL,
		BearerSrc:    fakeBearer{tok: "t"},
		OnSessionDie: func(string) { died = true },
	})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()

	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if died {
		t.Error("OnSessionDie should not fire for a generic 5xx")
	}
	var errResp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if errResp["type"] != "error" {
		t.Errorf("response type = %v, want error", errResp["type"])
	}
}

// ── TestTerminal_BearerFreshError ────────────────────────────────────────────

func TestTerminal_BearerFreshError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be reached when Fresh() fails")
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{
		UpstreamURL: up.URL,
		BearerSrc:   fakeBearer{err: errors.New("refresh failed")},
	})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()

	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}

// ── TestTerminal_401RetryFreshErrorReturns502 ────────────────────────────────

func TestTerminal_401RetryFreshErrorReturns502(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()

	calls := 0
	bearer := bearerFunc(func() (string, error) {
		calls++
		if calls == 1 {
			return "tok", nil
		}
		return "", errors.New("refresh failed on retry")
	})
	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: bearer})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()

	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}

type bearerFunc func() (string, error)

func (f bearerFunc) Fresh() (string, error) { return f() }

// ── TestTerminal_BodyReadError ───────────────────────────────────────────────

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("read error") }
func (errReader) Close() error               { return nil }

func TestTerminal_BodyReadError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be reached on body read error")
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Body = errReader{}
	rr := httptest.NewRecorder()

	// Skip AliasRewrite (it would itself hit the same read error and
	// short-circuit at 400 before the terminal ever runs) and drive the
	// terminal directly, exercising its own io.ReadAll error branch.
	term.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ── TestTerminal_UpstreamConnectionError ─────────────────────────────────────

func TestTerminal_UpstreamConnectionError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()

	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}

// ── TestNewTerminal_Defaults ──────────────────────────────────────────────────

func TestNewTerminal_Defaults(t *testing.T) {
	term := NewTerminal(TerminalOpts{})
	if term == nil {
		t.Fatal("NewTerminal returned nil")
	}
	if term.opts.UpstreamURL != "https://api.x.ai" {
		t.Errorf("UpstreamURL = %q, want https://api.x.ai", term.opts.UpstreamURL)
	}
	if term.opts.DefaultModel != "grok-composer-2.5-fast" {
		t.Errorf("DefaultModel = %q, want grok-composer-2.5-fast", term.opts.DefaultModel)
	}
	if term.opts.OnSessionDie == nil {
		t.Error("OnSessionDie default is nil")
	}
	term.opts.OnSessionDie("reason") // must not panic
	if term.opts.Transport == nil {
		t.Error("Transport default is nil")
	}
}

// ── TestShouldDieFast ──────────────────────────────────────────────────────
// Direct unit tests for the die-fast heuristic's defensive branches, mirroring
// internal/codex/middleware's coverage of the same logic shape.

func TestShouldDieFast_MalformedJSON(t *testing.T) {
	if shouldDieFast([]byte("not json"), "grok-4.5") {
		t.Error("malformed JSON should not trigger die-fast")
	}
}

func TestShouldDieFast_ModelNotFoundCode(t *testing.T) {
	body := []byte(`{"error":{"code":"model_not_found","message":"x"}}`)
	if !shouldDieFast(body, "grok-4.5") {
		t.Error("model_not_found code should trigger die-fast")
	}
}

func TestShouldDieFast_InvalidRequestWithModelInMessage(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"model grok-4.5 is not available"}}`)
	if !shouldDieFast(body, "grok-4.5") {
		t.Error("invalid_request_error containing model name should trigger die-fast")
	}
}

func TestShouldDieFast_InvalidRequestNoModelMatch(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"something else broke"}}`)
	if shouldDieFast(body, "grok-4.5") {
		t.Error("unrelated invalid_request_error should not trigger die-fast")
	}
}

func TestShouldDieFast_OtherErrorType(t *testing.T) {
	body := []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)
	if shouldDieFast(body, "grok-4.5") {
		t.Error("unrelated error type should not trigger die-fast")
	}
}

// ── TestOrderPreservingRewrite fallbacks (direct unit tests) ────────────────
// These branches are defensive and not reachable via normal well-formed
// Anthropic request bodies (the client always sends a well-formed "model"
// string field); exercised directly to satisfy the coverage bar, mirroring
// internal/share/middleware's TestRewriteModelField_Fallbacks.

func TestOrderPreservingRewrite_NoModelKey(t *testing.T) {
	result := orderPreservingRewrite([]byte(`{"messages":[]}`), "grok-4.5")
	if !bytes.Equal(result, []byte(`{"messages":[]}`)) {
		t.Errorf("body changed unexpectedly: %s", result)
	}
}

func TestOrderPreservingRewrite_NoColon(t *testing.T) {
	result := orderPreservingRewrite([]byte(`{"model"`), "grok-4.5")
	if !bytes.Equal(result, []byte(`{"model"`)) {
		t.Errorf("body changed unexpectedly: %s", result)
	}
}

func TestOrderPreservingRewrite_NoFirstQuote(t *testing.T) {
	result := orderPreservingRewrite([]byte(`{"model":123}`), "grok-4.5")
	if !bytes.Equal(result, []byte(`{"model":123}`)) {
		t.Errorf("body changed unexpectedly: %s", result)
	}
}

func TestOrderPreservingRewrite_NoSecondQuote(t *testing.T) {
	result := orderPreservingRewrite([]byte(`{"model":"unterminated`), "grok-4.5")
	if !bytes.Equal(result, []byte(`{"model":"unterminated`)) {
		t.Errorf("body changed unexpectedly: %s", result)
	}
}

// ── xAI tool-schema compatibility (ensureToolRequired) ────────────────────────

func TestEnsureToolRequired_InjectsMissingRequired(t *testing.T) {
	body := []byte(`{"model":"m","tools":[` +
		`{"name":"a","input_schema":{"type":"object","properties":{"x":{"type":"string"}}}},` +
		`{"name":"b","input_schema":{"type":"object","required":["y"],"properties":{"y":{"type":"string"}}}}` +
		`],"messages":[]}`)
	out := ensureToolRequired(body)

	var m struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Required *[]string `json:"required"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if m.Tools[0].InputSchema.Required == nil {
		t.Errorf("tool a: required should have been injected as an array")
	}
	if len(*m.Tools[0].InputSchema.Required) != 0 {
		t.Errorf("tool a: injected required should be empty, got %v", *m.Tools[0].InputSchema.Required)
	}
	if m.Tools[1].InputSchema.Required == nil || len(*m.Tools[1].InputSchema.Required) != 1 {
		t.Errorf("tool b: existing required must be preserved, got %v", m.Tools[1].InputSchema.Required)
	}
}

func TestEnsureToolRequired_NoToolSchema_Unchanged(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if out := ensureToolRequired(body); !bytes.Equal(out, body) {
		t.Errorf("body without tool schemas must be returned unchanged (order-preserving)")
	}
}

func TestEnsureToolRequired_AllRequiredPresent_Unchanged(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a","input_schema":{"type":"object","required":["x"]}}]}`)
	if out := ensureToolRequired(body); !bytes.Equal(out, body) {
		t.Errorf("body whose tools all have required must be returned unchanged")
	}
}

func TestEnsureToolRequired_BadJSON_Unchanged(t *testing.T) {
	body := []byte(`{"input_schema": not json`)
	if out := ensureToolRequired(body); !bytes.Equal(out, body) {
		t.Errorf("unparseable body must be returned unchanged (best-effort)")
	}
}

func TestTerminal_InjectsToolRequired_EndToEnd(t *testing.T) {
	var gotRequiredPresent bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct {
			Tools []struct {
				InputSchema struct {
					Required *[]string `json:"required"`
				} `json:"input_schema"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(b, &m)
		gotRequiredPresent = len(m.Tools) == 1 && m.Tools[0].InputSchema.Required != nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "tok"}})
	body := `{"model":"claude-sonnet","tools":[{"name":"a","input_schema":{"type":"object","properties":{}}}],"messages":[]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()
	withAlias(t, "claude-sonnet=grok-4.5", term, req, rr)

	if !gotRequiredPresent {
		t.Fatal("upstream should have received a tool schema with an injected required array")
	}
}

func TestEnsureToolRequired_ToolsNotArray_Unchanged(t *testing.T) {
	// Has the "input_schema" substring (passes the fast-path guard) and is
	// valid JSON, but tools is not an array → returned unchanged.
	body := []byte(`{"input_schema":0,"tools":{"nope":true}}`)
	if out := ensureToolRequired(body); !bytes.Equal(out, body) {
		t.Errorf("non-array tools must be returned unchanged")
	}
}

func TestEnsureToolRequired_MalformedToolEntries_Unchanged(t *testing.T) {
	// Tool entries that aren't objects, or whose input_schema isn't an
	// object, are skipped; with nothing to fix the body is unchanged.
	body := []byte(`{"tools":[123,{"name":"a","input_schema":"notobj"}]}`)
	if out := ensureToolRequired(body); !bytes.Equal(out, body) {
		t.Errorf("malformed tool entries must be skipped, body unchanged")
	}
}

// ── context-overflow → reactive-compact translation ──────────────────────────

func TestDetectContextOverflow(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		want     bool
		in, max  int
	}{
		{"openai-nested", `{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 131072 tokens. However, your messages resulted in 200000 tokens."}}`, true, 200000, 131072},
		{"xai-flat", `{"code":"context_length_exceeded","error":"maximum context length is 256000 tokens, but the request has 300000 tokens"}`, true, 300000, 256000},
		{"generic-too-long", `{"code":"invalid_request_error","error":"prompt is too long"}`, true, 0, 0},
		{"not-overflow-required", `{"code":"invalid-argument","error":"Invalid request content: Schema validation failed: /required: null is not of type array"}`, false, 0, 0},
		{"not-overflow-modelnf", `{"error":{"code":"model_not_found","message":"unknown model"}}`, false, 0, 0},
		{"empty", ``, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ov, in, max := detectContextOverflow([]byte(c.body))
			if ov != c.want {
				t.Fatalf("overflow = %v, want %v", ov, c.want)
			}
			if ov && (in != c.in || max != c.max) {
				t.Errorf("tokens = %d/%d, want %d/%d", in, max, c.in, c.max)
			}
		})
	}
}

func TestTerminal_OverflowTranslatesToPromptTooLong(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"context_length_exceeded","error":"maximum context length is 256000 tokens, but the request has 300000 tokens"}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()
	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var out struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if out.Error.Type != "invalid_request_error" {
		t.Errorf("error type = %q, want invalid_request_error", out.Error.Type)
	}
	if !strings.Contains(out.Error.Message, "prompt is too long") {
		t.Errorf("message = %q, want it to contain 'prompt is too long' (triggers reactive compact)", out.Error.Message)
	}
}

func TestTerminal_NonOverflow400StaysApiError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid-argument","error":"some other problem"}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	rr := httptest.NewRecorder()
	withAlias(t, "", term, req, rr)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "prompt is too long") {
		t.Errorf("non-overflow 400 must not be rewritten to prompt-too-long: %s", rr.Body.String())
	}
}
