package middleware

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/andybalholm/brotli"

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

// ── TestTerminal_SendsGrokShellIdentity ──────────────────────────────────────

func TestTerminal_SendsGrokShellIdentity(t *testing.T) {
	var gotPath, gotUA, gotIdent, gotTokenAuth, gotModelOverride, gotAuth, gotConv string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		gotIdent = r.Header.Get("x-grok-client-identifier")
		gotTokenAuth = r.Header.Get("x-xai-token-auth")
		gotModelOverride = r.Header.Get("x-grok-model-override")
		gotAuth = r.Header.Get("Authorization")
		gotConv = r.Header.Get("x-grok-conv-id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "tok"}})
	body := `{"model":"claude-sonnet","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Claude-Code-Session-Id", "sess-xyz")
	rr := httptest.NewRecorder()
	withAlias(t, "claude-sonnet=grok-4.5", term, req, rr)

	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
	if !strings.HasPrefix(gotUA, "grok-shell/") {
		t.Errorf("User-Agent = %q, want grok-shell/ prefix (not the old fake UA)", gotUA)
	}
	if gotIdent != "grok-shell" || gotTokenAuth != "xai-grok-cli" {
		t.Errorf("identity headers wrong: ident=%q tokenAuth=%q", gotIdent, gotTokenAuth)
	}
	if gotModelOverride != "grok-4.5" {
		t.Errorf("x-grok-model-override = %q, want grok-4.5", gotModelOverride)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
	}
	if gotConv != "sess-xyz" {
		t.Errorf("x-grok-conv-id = %q, want the session id", gotConv)
	}
}

func TestTerminal_TurnIdxIncrementsPerSession(t *testing.T) {
	var mu sync.Mutex
	var turns []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		turns = append(turns, r.Header.Get("x-grok-turn-idx"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
		req.Header.Set("X-Claude-Code-Session-Id", "s")
		withAlias(t, "", term, req, httptest.NewRecorder())
	}
	if len(turns) != 2 || turns[0] != "1" || turns[1] != "2" {
		t.Fatalf("turn-idx per session should be 1 then 2, got %v", turns)
	}
}

func TestBodyStreams_MalformedJSON_DefaultsTrue(t *testing.T) {
	if !bodyStreams([]byte(`not json`)) {
		t.Error("malformed body should default to streaming (true)")
	}
}

func TestBodyStreams_AbsentDefaultsTrue(t *testing.T) {
	if !bodyStreams([]byte(`{"model":"x"}`)) {
		t.Error("absent stream field should default to true")
	}
}

func TestBodyStreams_FalseHonored(t *testing.T) {
	if bodyStreams([]byte(`{"model":"x","stream":false}`)) {
		t.Error("stream:false should be honored")
	}
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
	if term.opts.UpstreamURL != "https://cli-chat-proxy.grok.com" {
		t.Errorf("UpstreamURL = %q, want https://cli-chat-proxy.grok.com", term.opts.UpstreamURL)
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

// ── hoist role:"system" messages into the top-level system field ─────────────

func TestHoistSystemMessages_MovesSystemRoleToTopLevel(t *testing.T) {
	body := []byte(`{"model":"m","system":[{"type":"text","text":"base"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"hook ctx"}]}`)
	out := hoistSystemMessages(body)
	var m struct {
		System []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("bad output: %v", err)
	}
	if len(m.Messages) != 1 || m.Messages[0].Role != "user" {
		t.Fatalf("messages should be [user], got %+v", m.Messages)
	}
	if len(m.System) != 2 || m.System[1].Type != "text" || m.System[1].Text != "hook ctx" {
		t.Fatalf("system should be base + hoisted text block, got %+v", m.System)
	}
}

func TestHoistSystemMessages_StringSystem(t *testing.T) {
	body := []byte(`{"system":"base","messages":[{"role":"system","content":"ctx"}]}`)
	out := hoistSystemMessages(body)
	var m struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []any `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("bad output: %v", err)
	}
	if len(m.System) != 2 || m.System[0].Text != "base" || m.System[1].Text != "ctx" {
		t.Fatalf("string system should convert+append, got %+v", m.System)
	}
	if len(m.Messages) != 0 {
		t.Fatalf("system-role message should be removed, got %d messages", len(m.Messages))
	}
}

func TestHoistSystemMessages_NoSystemRole_Unchanged(t *testing.T) {
	body := []byte(`{"system":"s","messages":[{"role":"user","content":"hi"}]}`)
	if out := hoistSystemMessages(body); !bytes.Equal(out, body) {
		t.Errorf("no system-role message must be returned unchanged")
	}
}

func TestHoistSystemMessages_BadJSON_Unchanged(t *testing.T) {
	body := []byte(`{"role":"system" not json`)
	if out := hoistSystemMessages(body); !bytes.Equal(out, body) {
		t.Errorf("unparseable body must be returned unchanged")
	}
}

func TestTerminal_HoistsSystemRole_EndToEnd(t *testing.T) {
	var roles []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(b, &m)
		roles = nil
		for _, msg := range m.Messages {
			roles = append(roles, msg.Role)
		}
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	body := `{"model":"claude-sonnet","system":"base","messages":[{"role":"user","content":"hi"},{"role":"system","content":"ctx"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()
	withAlias(t, "", term, req, rr)

	if len(roles) != 1 || roles[0] != "user" {
		t.Fatalf("upstream should see only [user] after hoist, got %v", roles)
	}
}

func TestHoistSystemMessages_ArrayContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"blk"}]}]}`)
	out := hoistSystemMessages(body)
	var m struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.System) != 1 || m.System[0].Text != "blk" {
		t.Fatalf("array content should hoist as blocks, got %+v", m.System)
	}
}

func TestHoistSystemMessages_MessagesNotArray_Unchanged(t *testing.T) {
	body := []byte(`{"role":"system","messages":5}`)
	if out := hoistSystemMessages(body); !bytes.Equal(out, body) {
		t.Errorf("non-array messages must be returned unchanged")
	}
}

func TestHoistSystemMessages_OddEntriesAndContent_Unchanged(t *testing.T) {
	// A non-object message entry is skipped; a system message with
	// non-string/array content yields nothing to hoist, so the body is
	// returned unchanged (nothing actionable).
	body := []byte(`{"messages":[123,{"role":"system","content":42}]}`)
	if out := hoistSystemMessages(body); !bytes.Equal(out, body) {
		t.Errorf("nothing hoistable → unchanged, got %s", out)
	}
}

// ── clamp output_config.effort xhigh → high (grok has only low/medium/high) ──

func TestClampEffort_XhighToHigh(t *testing.T) {
	body := []byte(`{"model":"m","output_config":{"effort":"xhigh"},"messages":[]}`)
	out := clampEffort(body)
	var m struct {
		OC struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m.OC.Effort != "high" {
		t.Fatalf("effort should be clamped to high, got %q", m.OC.Effort)
	}
}

func TestClampEffort_HighUnchanged(t *testing.T) {
	body := []byte(`{"output_config":{"effort":"high"}}`)
	if out := clampEffort(body); !bytes.Equal(out, body) {
		t.Errorf("non-xhigh effort must be returned unchanged")
	}
}

func TestClampEffort_NoXhigh_Unchanged(t *testing.T) {
	body := []byte(`{"output_config":{"effort":"medium"},"messages":[]}`)
	if out := clampEffort(body); !bytes.Equal(out, body) {
		t.Errorf("body without xhigh must be returned unchanged")
	}
}

func TestClampEffort_XhighInContentNotConfig_Unchanged(t *testing.T) {
	// "xhigh" appears in message text, not output_config.effort → no change.
	body := []byte(`{"messages":[{"role":"user","content":"what is xhigh"}]}`)
	if out := clampEffort(body); !bytes.Equal(out, body) {
		t.Errorf("xhigh outside output_config.effort must not mutate the body")
	}
}

func TestClampEffort_BadJSON_Unchanged(t *testing.T) {
	body := []byte(`{"effort":"xhigh" not json`)
	if out := clampEffort(body); !bytes.Equal(out, body) {
		t.Errorf("unparseable body must be returned unchanged")
	}
}

func TestClampEffort_XhighElsewhere_ConfigNotXhigh_Unchanged(t *testing.T) {
	// The quoted "xhigh" appears as another field's value (guard passes),
	// but output_config.effort is not xhigh → nothing is clamped.
	body := []byte(`{"output_config":{"effort":"high"},"marker":"xhigh"}`)
	if out := clampEffort(body); !bytes.Equal(out, body) {
		t.Errorf("must not mutate when output_config.effort is not xhigh, got %s", out)
	}
}

// ── decodeBody: undo grok-shell's Accept-Encoding on error-path bodies ───────
//
// ccm sends grok-shell's authentic "Accept-Encoding: gzip, br, deflate", which
// disables Go's transparent response decompression. The error path inspects
// the body (shouldDieFast, detectContextOverflow), so it must inflate it
// itself; decodeBody is that inflate step.

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func brotliBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	bw := brotli.NewWriter(&buf)
	if _, err := bw.Write([]byte(s)); err != nil {
		t.Fatalf("brotli write: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("brotli close: %v", err)
	}
	return buf.Bytes()
}

func zlibBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

// rawDeflateBytes builds a raw RFC 1951 DEFLATE stream (no zlib wrapper) —
// what some servers send for "Content-Encoding: deflate" despite the HTTP
// spec nominally meaning zlib-wrapped.
func rawDeflateBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("flate.NewWriter: %v", err)
	}
	if _, err := fw.Write([]byte(s)); err != nil {
		t.Fatalf("flate write: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("flate close: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeBody(t *testing.T) {
	const want = `{"ok":1}`

	header := func(encoding string) http.Header {
		h := http.Header{}
		if encoding != "" {
			h.Set("Content-Encoding", encoding)
		}
		return h
	}

	t.Run("gzip", func(t *testing.T) {
		got := decodeBody(header("gzip"), gzipBytes(t, want))
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("br", func(t *testing.T) {
		got := decodeBody(header("br"), brotliBytes(t, want))
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("deflate", func(t *testing.T) {
		got := decodeBody(header("deflate"), zlibBytes(t, want))
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty_passthrough", func(t *testing.T) {
		raw := []byte(want)
		got := decodeBody(header(""), raw)
		if !bytes.Equal(got, raw) {
			t.Errorf("got %q, want raw passthrough %q", got, raw)
		}
	})

	t.Run("identity_passthrough", func(t *testing.T) {
		raw := []byte(want)
		got := decodeBody(header("identity"), raw)
		if !bytes.Equal(got, raw) {
			t.Errorf("got %q, want raw passthrough %q", got, raw)
		}
	})

	t.Run("unknown_encoding_passthrough", func(t *testing.T) {
		raw := []byte(want)
		got := decodeBody(header("weird"), raw)
		if !bytes.Equal(got, raw) {
			t.Errorf("got %q, want raw passthrough %q", got, raw)
		}
	})

	t.Run("gzip_header_garbage_body_passthrough", func(t *testing.T) {
		raw := []byte("not actually gzip")
		got := decodeBody(header("gzip"), raw)
		if !bytes.Equal(got, raw) {
			t.Errorf("got %q, want raw passthrough %q", got, raw)
		}
	})

	t.Run("gzip_valid_header_truncated_body_passthrough", func(t *testing.T) {
		// A valid gzip header (so gzip.NewReader succeeds) truncated before
		// the stream's end, so gzip.NewReader succeeds but io.ReadAll fails
		// (unexpected EOF) — exercises the post-header inflate-error branch.
		full := gzipBytes(t, want)
		truncated := full[:len(full)-4]
		got := decodeBody(header("gzip"), truncated)
		if !bytes.Equal(got, truncated) {
			t.Errorf("got %q, want raw passthrough %q", got, truncated)
		}
	})

	t.Run("br_garbage_body_passthrough", func(t *testing.T) {
		// brotli.NewReader never errors on construction (lazy decode); the
		// failure surfaces from io.ReadAll on invalid stream bytes.
		raw := []byte("not actually brotli, just plain garbage bytes")
		got := decodeBody(header("br"), raw)
		if !bytes.Equal(got, raw) {
			t.Errorf("got %q, want raw passthrough %q", got, raw)
		}
	})

	t.Run("deflate_raw_rfc1951_fallback", func(t *testing.T) {
		// Some servers send raw DEFLATE (no zlib wrapper) for
		// "Content-Encoding: deflate" — zlib.NewReader rejects the missing
		// header, so decodeBody must fall back to the raw flate reader.
		got := decodeBody(header("deflate"), rawDeflateBytes(t, want))
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("deflate_garbage_body_passthrough", func(t *testing.T) {
		// Neither zlib nor raw flate can decode this — both fallbacks are
		// exhausted and decodeBody returns the raw bytes.
		raw := []byte("not zlib and not raw deflate either")
		got := decodeBody(header("deflate"), raw)
		if !bytes.Equal(got, raw) {
			t.Errorf("got %q, want raw passthrough %q", got, raw)
		}
	})
}

// ── error-path bodies arrive compressed (grok-shell Accept-Encoding) ─────────

func TestTerminal_GzipErrorBody_DieFast(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(gzipBytes(t, `{"error":{"code":"model_not_found","message":"nope"}}`))
	}))
	defer up.Close()

	var died atomic.Value
	term := NewTerminal(TerminalOpts{
		UpstreamURL:  up.URL,
		BearerSrc:    fakeBearer{tok: "t"},
		OnSessionDie: func(reason string) { died.Store(reason) },
	})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("X-Claude-Code-Session-Id", "sess-1")
	rr := httptest.NewRecorder()

	withAlias(t, "x=grok-4.5", term, req, rr)

	got := died.Load()
	if got == nil {
		t.Fatal("OnSessionDie not called — die-fast must survive a gzip-compressed error body")
	}
	if !strings.Contains(got.(string), "grok-4.5") {
		t.Errorf("die reason = %v, want to mention target model", got)
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestTerminal_GzipErrorBody_OverflowTranslated(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(gzipBytes(t, `{"error":{"message":"This model's maximum context length is 500000 tokens, however you requested 620000 tokens"}}`))
	}))
	defer up.Close()

	term := NewTerminal(TerminalOpts{UpstreamURL: up.URL, BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("X-Claude-Code-Session-Id", "sess-1")
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
	if !strings.Contains(out.Error.Message, "prompt is too long") {
		t.Errorf("message = %q, want it to contain 'prompt is too long' (reactive-compaction trigger survives a gzip-compressed error body)", out.Error.Message)
	}
}
