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
	"strconv"
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

// flushRecorder wraps httptest.ResponseRecorder to also implement
// http.Flusher, so we can assert the terminal's flush-per-write path runs.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int32
}

func (f *flushRecorder) Flush() {
	atomic.AddInt32(&f.flushes, 1)
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

type bearerFunc func() (string, error)

func (f bearerFunc) Fresh() (string, error) { return f() }

// ── TestTerminal_BodyReadError ───────────────────────────────────────────────

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("read error") }
func (errReader) Close() error               { return nil }

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

func TestDetectContextOverflow(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    bool
		in, max int
	}{
		{"openai-nested", `{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 131072 tokens. However, your messages resulted in 200000 tokens."}}`, true, 200000, 131072},
		{"xai-flat", `{"code":"context_length_exceeded","error":"maximum context length is 256000 tokens, but the request has 300000 tokens"}`, true, 300000, 256000},
		// The real grok-4.5 overflow shape (cli-chat-proxy, 2026-07-14). Uses
		// "maximum prompt length", which the original marker set missed → the
		// raw 400 leaked to Claude Code instead of triggering auto-compaction.
		{"grok-prompt-length", `{"code":"invalid-argument","error":"This model's maximum prompt length is 500000 but the request contains 500317 tokens."}`, true, 500317, 500000},
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

// response.completed with the given input token count.
func responsesSSE(text string, inputTokens int) string {
	return "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":" + strconv.Quote(text) + "}\n\n" +
		"data: {\"type\":\"response.output_text.done\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":" + strconv.Itoa(inputTokens) + ",\"output_tokens\":1}}}\n\n"
}

// fakeGrok is an httptest upstream that records requests and answers with
// responsesSSE (or a custom handler when set).
type fakeGrok struct {
	mu       sync.Mutex
	paths    []string
	headers  []http.Header
	bodies   []map[string]any
	window   string
	tokens   int
	override http.HandlerFunc
}

func (f *fakeGrok) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.headers = append(f.headers, r.Header.Clone())
	f.bodies = append(f.bodies, m)
	f.mu.Unlock()
	if f.override != nil {
		f.override(w, r)
		return
	}
	if f.window != "" {
		w.Header().Set("x-grok-context-window", f.window)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, responsesSSE("pong", f.tokens))
}

func newFakeGrok(t *testing.T) (*fakeGrok, *httptest.Server) {
	f := &fakeGrok{tokens: 10}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, srv
}

func post(t *testing.T, term *Terminal, aliasRule, session, body string) *flushRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	if session != "" {
		req.Header.Set("X-Claude-Code-Session-Id", session)
	}
	rr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	withAlias(t, aliasRule, term, req, rr)
	return rr
}

// errMessageOf returns error.message from an Anthropic error body;
// json.Marshal escapes ">" as \u003e, so raw substring checks would miss it.
func errMessageOf(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	return e.Error.Message
}

const userHi = `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`

func TestTerminal_PostsTranslatedRequestToResponses(t *testing.T) {
	f, srv := newFakeGrok(t)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "tok"}, CredentialID: "cred-1"})
	rr := post(t, term, "claude-opus-*=grok-4.6", "sess-1",
		`{"model":"claude-opus-5-5","system":"be terse","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`)

	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if f.paths[0] != "/v1/responses" {
		t.Errorf("path = %q, want /v1/responses", f.paths[0])
	}
	b := f.bodies[0]
	if b["model"] != "grok-4.6" || b["prompt_cache_key"] != "sess-1" || b["stream"] != true {
		t.Errorf("body = %v", b)
	}
	if first := b["input"].([]any)[0].(map[string]any); first["role"] != "system" || first["content"] != "be terse" {
		t.Errorf("input[0] = %v", first)
	}
	h := f.headers[0]
	if h.Get("Authorization") != "Bearer tok" || h.Get("x-grok-model-override") != "grok-4.6" || h.Get("x-grok-agent-id") == "" {
		t.Errorf("headers = %v", h)
	}
	if !strings.Contains(rr.Body.String(), `"text":"pong"`) || !strings.Contains(rr.Body.String(), "message_stop") {
		t.Errorf("client SSE = %s", rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type = %q", rr.Header().Get("Content-Type"))
	}
}

func TestTerminal_DefaultModelGrok47(t *testing.T) {
	f, srv := newFakeGrok(t)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	post(t, term, "", "s", userHi)
	if f.bodies[0]["model"] != "grok-4.7" {
		t.Errorf("model = %v, want grok-4.7", f.bodies[0]["model"])
	}
}

func TestTerminal_TurnIdxCountsUserTurnsNotRequests(t *testing.T) {
	f, srv := newFakeGrok(t)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	toolResult := `{"model":"m","messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`
	trailingSystem := `{"model":"m","messages":[{"role":"user","content":"next"},{"role":"system","content":"hook"}]}`
	post(t, term, "", "s", userHi)         // turn 1
	post(t, term, "", "s", toolResult)     // still turn 1
	post(t, term, "", "s", trailingSystem) // turn 2 (trailing system skipped)
	post(t, term, "", "other", toolResult) // first request of a session is turn 1
	got := []string{}
	for _, h := range f.headers {
		got = append(got, h.Get("x-grok-turn-idx"))
	}
	if strings.Join(got, ",") != "1,1,2,1" {
		t.Errorf("turn-idx sequence = %v, want 1,1,2,1", got)
	}
}

func TestIsNewUserTurn(t *testing.T) {
	for body, want := range map[string]bool{
		`{"messages":[{"role":"user","content":"hi"}]}`:                                       true,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`:              true,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x"}]}]}`: false,
		`{"messages":[{"role":"assistant","content":"a"}]}`:                                   false,
		`{"messages":[{"role":"user","content":"hi"},{"role":"system","content":"s"}]}`:       true,
		`{"messages":[{"role":"system","content":"s"}]}`:                                      false,
		`{"messages":[]}`: false,
		`not json`:        false,
	} {
		if got := isNewUserTurn([]byte(body)); got != want {
			t.Errorf("isNewUserTurn(%s) = %v, want %v", body, got, want)
		}
	}
}

func TestTerminal_ReasoningRoundTripsThroughThinkingSignature(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"+
			"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_9\"}}\n\n"+
			"data: {\"type\":\"response.reasoning_summary_part.added\"}\n\n"+
			"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"secret plan\"}\n\n"+
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_9\",\"summary\":[],\"encrypted_content\":\"ENC9\"}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n")
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := post(t, term, "", "s", `{"model":"m","thinking":{"type":"adaptive","display":"omitted"},"messages":[{"role":"user","content":"q"}]}`)
	out := rr.Body.String()
	if strings.Contains(out, "secret plan") {
		t.Error("display:omitted must suppress thinking text")
	}
	i := strings.Index(out, `"signature":"`)
	if i < 0 {
		t.Fatalf("no signature in %s", out)
	}
	sig := out[i+len(`"signature":"`):]
	sig = sig[:strings.IndexByte(sig, '"')]

	f.override = nil
	post(t, term, "", "s", `{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"`+sig+`"}]},{"role":"user","content":"more"}]}`)
	in := f.bodies[1]["input"].([]any)
	r := in[1].(map[string]any)
	if r["type"] != "reasoning" || r["id"] != "rs_9" || r["encrypted_content"] != "ENC9" {
		t.Errorf("replayed reasoning item = %v", r)
	}
}

// After a model switch (alias now maps to a different grok model) the
// reasoning minted by the previous model is not replayed.
func TestTerminal_ReasoningNotReplayedAcrossModels(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"+
			"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_9\"}}\n\n"+
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_9\",\"summary\":[],\"encrypted_content\":\"ENC9\"}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n")
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	out := post(t, term, "claude-opus-*=grok-4.7", "s", `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"q"}]}`).Body.String()
	i := strings.Index(out, `"signature":"`)
	if i < 0 {
		t.Fatalf("no signature in %s", out)
	}
	sig := out[i+len(`"signature":"`):]
	sig = sig[:strings.IndexByte(sig, '"')]

	f.override = nil
	next := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + sig + `"},{"type":"text","text":"a"}]},{"role":"user","content":"more"}]}`
	post(t, term, "claude-opus-*=grok-4.7", "s", next)
	post(t, term, "claude-opus-*=grok-4.6", "s", next)
	hasReasoning := func(body map[string]any) bool {
		for _, it := range body["input"].([]any) {
			if it.(map[string]any)["type"] == "reasoning" {
				return true
			}
		}
		return false
	}
	if !hasReasoning(f.bodies[1]) {
		t.Error("same model: reasoning must be replayed")
	}
	if hasReasoning(f.bodies[2]) {
		t.Error("different model: reasoning must be dropped")
	}
}

func TestTerminal_NonStreamReturnsMessageJSON(t *testing.T) {
	_, srv := newFakeGrok(t)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := post(t, term, "", "s", `{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %s", rr.Body.String())
	}
	if m["type"] != "message" || m["content"].([]any)[0].(map[string]any)["text"] != "pong" {
		t.Errorf("message = %v", m)
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q", rr.Header().Get("Content-Type"))
	}
}

func TestTerminal_NonStreamAssembleErrorInline(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n") // no response.created → no message
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := post(t, term, "", "s", `{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(rr.Body.String(), `"type":"error"`) {
		t.Errorf("want inline error JSON, got %s", rr.Body.String())
	}
}

func TestTerminal_ProactiveCompactAtThresholdThenClears(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.window, f.tokens = "1000", 950
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	post(t, term, "", "s", userHi) // records 950/1000
	rr := post(t, term, "", "s", userHi)
	if rr.Code != 400 || !strings.Contains(errMessageOf(rr.Body.Bytes()), "prompt is too long: 950 tokens > 1000 maximum") {
		t.Fatalf("want proactive 400, got %d %s", rr.Code, rr.Body.String())
	}
	if len(f.paths) != 1 {
		t.Errorf("gated request must not reach upstream; hits = %d", len(f.paths))
	}
	f.tokens = 100
	if rr := post(t, term, "", "s", userHi); rr.Code != 200 {
		t.Errorf("post-compaction retry must forward, got %d", rr.Code)
	}
}

func TestTerminal_UnderThresholdAlwaysForwards(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.window, f.tokens = "1000", 900
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	post(t, term, "", "s", userHi)
	if rr := post(t, term, "", "s", userHi); rr.Code != 200 {
		t.Errorf("90%% must forward, got %d", rr.Code)
	}
}

func TestRecordCtx_NoOpOnMissingData(t *testing.T) {
	term := NewTerminal(TerminalOpts{BearerSrc: fakeBearer{tok: "t"}})
	term.recordCtx("", 10, 10)
	term.recordCtx("s", 10, 0)
	term.recordCtx("s", 0, 10)
	if _, _, over := term.overCtx("s"); over {
		t.Error("nothing should be recorded")
	}
	if _, _, over := term.overCtx(""); over {
		t.Error("empty session never gates")
	}
}

func TestTerminal_OverflowErrorTranslatesToPromptTooLong(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"code":"invalid-argument","error":"This model's maximum prompt length is 500000 but the request contains 500317 tokens."}`)
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := post(t, term, "", "s", userHi)
	if rr.Code != 400 || !strings.Contains(errMessageOf(rr.Body.Bytes()), "prompt is too long: 500317 tokens > 500000 maximum") {
		t.Errorf("got %d %s", rr.Code, rr.Body.String())
	}
}

func TestTerminal_GzipErrorBody_DieFast(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(404)
		_, _ = w.Write(gzipBytes(t, `{"error":{"code":"model_not_found","message":"no"}}`))
	}
	died := make(chan string, 1)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}, OnSessionDie: func(r string) { died <- r }})
	rr := post(t, term, "", "s", userHi)
	if rr.Code != 404 {
		t.Errorf("status = %d", rr.Code)
	}
	select {
	case r := <-died:
		if !strings.Contains(r, "grok-4.7") {
			t.Errorf("reason = %q", r)
		}
	default:
		t.Error("OnSessionDie not called")
	}
}

func TestTerminal_OtherErrorRelayedAsAPIError(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(426)
		_, _ = io.WriteString(w, `{"error":"Your Grok CLI version (0.2.101) is outdated."}`)
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := post(t, term, "", "s", userHi)
	if rr.Code != 426 || !strings.Contains(rr.Body.String(), "api_error") || !strings.Contains(rr.Body.String(), "outdated") {
		t.Errorf("got %d %s", rr.Code, rr.Body.String())
	}
}

func TestTerminal_401RefreshRetry(t *testing.T) {
	f, srv := newFakeGrok(t)
	var n atomic.Int32
	f.override = func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesSSE("after refresh", 1))
	}
	cb := &countingBearer{tok: "stale"}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: cb})
	rr := post(t, term, "", "s", userHi)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "after refresh") || n.Load() != 2 {
		t.Errorf("code=%d hits=%d body=%s", rr.Code, n.Load(), rr.Body.String())
	}
}

func TestTerminal_BearerFreshError(t *testing.T) {
	_, srv := newFakeGrok(t)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{err: errors.New("no token")}})
	if rr := post(t, term, "", "s", userHi); rr.Code != 502 {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

func TestTerminal_401RetryFreshErrorReturns502(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }
	calls := 0
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: bearerFunc(func() (string, error) {
		calls++
		if calls > 1 {
			return "", errors.New("refresh failed")
		}
		return "t", nil
	})})
	if rr := post(t, term, "", "s", userHi); rr.Code != 502 {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

func TestTerminal_InvalidBodyIs400(t *testing.T) {
	_, srv := newFakeGrok(t)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	if rr := post(t, term, "", "s", `{not json`); rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTerminal_BodyReadError(t *testing.T) {
	term := NewTerminal(TerminalOpts{BearerSrc: fakeBearer{tok: "t"}})
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Body = errReader{}
	rr := httptest.NewRecorder()
	term.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTerminal_UpstreamConnectionError(t *testing.T) {
	term := NewTerminal(TerminalOpts{UpstreamURL: "http://127.0.0.1:1", BearerSrc: fakeBearer{tok: "t"}})
	if rr := post(t, term, "", "s", userHi); rr.Code != 502 {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTerminal_ClassifyOverflowTranslates(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"+
			"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":300000,\"output_tokens\":0}}}\n\n")
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := post(t, term, "", "s", userHi)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "prompt is too long") {
		t.Errorf("got %d %s", rr.Code, rr.Body.String())
	}
}

func TestTerminal_ClassifyReadErrorIs502(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "not gzip at all")
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	if rr := post(t, term, "", "s", userHi); rr.Code != 502 {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

func TestNewTerminal_Defaults(t *testing.T) {
	term := NewTerminal(TerminalOpts{})
	if term.opts.UpstreamURL != defaultUpstream || term.opts.DefaultModel != "grok-4.7" || term.opts.Transport == nil || term.opts.OnSessionDie == nil {
		t.Errorf("defaults = %+v", term.opts)
	}
	term.opts.OnSessionDie("x") // the no-op default must not panic
}

func TestBodyStreams(t *testing.T) {
	for body, want := range map[string]bool{`not json`: true, `{}`: true, `{"stream":true}`: true, `{"stream":false}`: false} {
		if got := bodyStreams([]byte(body)); got != want {
			t.Errorf("bodyStreams(%s) = %v", body, got)
		}
	}
}

func TestTerminal_DirectInvalidBodyAndDisplayModelFallback(t *testing.T) {
	// Without the alias middleware OriginalModel is empty, so the display
	// model falls back to the target model.
	_, srv := newFakeGrok(t)
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := httptest.NewRecorder()
	term.ServeHTTP(rr, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{not json`)))
	if rr.Code != 400 {
		t.Errorf("invalid body: status = %d, want 400", rr.Code)
	}
	rr = httptest.NewRecorder()
	term.ServeHTTP(rr, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(userHi)))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"model":"grok-4.7"`) {
		t.Errorf("got %d %s", rr.Code, rr.Body.String())
	}
}

func TestTerminal_StreamInterruptedEmitsErrorEvent(t *testing.T) {
	f, srv := newFakeGrok(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "100000") // promise more than is sent
		_, _ = io.WriteString(w, responsesSSE("part", 1)[:200])
	}
	term := NewTerminal(TerminalOpts{UpstreamURL: srv.URL, BearerSrc: fakeBearer{tok: "t"}})
	rr := post(t, term, "", "s", userHi)
	if !strings.Contains(rr.Body.String(), "event: error") || !strings.Contains(rr.Body.String(), "stream interrupted") {
		t.Errorf("want trailing error event, got %s", rr.Body.String())
	}
}
