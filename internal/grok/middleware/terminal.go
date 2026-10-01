// Package middleware provides the grok proxy terminal handler.
// cli-chat-proxy.grok.com serves grok-shell over OpenAI's Responses API
// (/v1/responses; grok-shell 1.0.46, captured 2026-10-01), so the terminal:
//  1. picks the target model (alias match, else DefaultModel)
//  2. proactively forces compaction near grok's context window
//  3. translates Anthropic Messages → Responses (responses.Grok dialect)
//  4. POSTs UpstreamURL/v1/responses presenting as grok-shell; 401 →
//     refresh + retry once (the bearer is only ever sent to grok)
//  5. on error: model_not_found → die-fast; overflow → "prompt is too long"
//  6. translates the Responses SSE back to Anthropic SSE (or one Message
//     JSON for stream:false) and records grok's prompt size for step 2
package middleware

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"

	"github.com/hbinhng/claude-credentials-manager/internal/responses"
	sharemw "github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/trace"
)

const (
	defaultUpstream = "https://cli-chat-proxy.grok.com"
	defaultModel    = "grok-4.7"
)

// ctxCompactThreshold is the fraction of grok's context window at which ccm
// proactively returns "prompt is too long" to force Claude Code compaction.
// x-grok-context-window (256000 on grok-4.7) is a policy window, not the
// hard limit — a 396k-token prompt was accepted (2026-10-01) — and
// grok-shell itself compacts at x-compaction-at 204800 (80%). Driven by
// grok's own counts (response usage), not an estimate.
const ctxCompactThreshold = 0.92

// TerminalOpts configures the grok terminal handler.
type TerminalOpts struct {
	// Transport is anything that can execute HTTP requests. Defaults to
	// trace.WrapDoer(&http.Client{}) when nil.
	Transport trace.Doer
	// UpstreamURL overrides the grok backend (default
	// "https://cli-chat-proxy.grok.com"). Test-only; production callers
	// leave it blank.
	UpstreamURL string
	// BearerSrc fetches the cred's current grok access token. On a 401
	// from upstream, the terminal re-fetches it and retries once.
	BearerSrc sharemw.BearerSource
	// OnSessionDie is called when a model_not_found error from upstream
	// triggers die-fast. Wired by share.Session to call proxy.Stop.
	OnSessionDie func(reason string)
	// DefaultModel is used when the inbound request's model did not
	// match an alias rule. Defaults to "grok-4.7".
	DefaultModel string
	// CredentialID is the ccm credential id; x-grok-agent-id derives from
	// it (stable per credential, like grok-shell's per-install id).
	CredentialID string
}

// Terminal is the grok-specific http.Handler that lives at the end of the
// share pipeline.
type Terminal struct {
	opts   TerminalOpts
	turnMu sync.Mutex
	turns  map[string]int
	ctxMu  sync.Mutex
	ctx    map[string]ctxState
}

// ctxState is the last observed prompt size and context window for a session,
// as reported by grok.
type ctxState struct {
	promptTokens int
	window       int
}

// NewTerminal constructs a Terminal with defaults applied.
func NewTerminal(opts TerminalOpts) *Terminal {
	if opts.UpstreamURL == "" {
		opts.UpstreamURL = defaultUpstream
	}
	if opts.DefaultModel == "" {
		opts.DefaultModel = defaultModel
	}
	if opts.OnSessionDie == nil {
		opts.OnSessionDie = func(string) {} // no-op default
	}
	if opts.Transport == nil {
		opts.Transport = trace.WrapDoer(&http.Client{})
	}
	return &Terminal{opts: opts, turns: map[string]int{}, ctx: map[string]ctxState{}}
}

// ServeHTTP implements http.Handler.
func (t *Terminal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "read body: "+err.Error())
		return
	}

	targetModel := t.opts.DefaultModel
	if sharemw.AliasMatched(r.Context()) {
		targetModel = sharemw.EffectiveModel(r.Context())
	}
	sessionID := r.Header.Get("X-Claude-Code-Session-Id")

	// Proactive compaction: if the previous turn on this session used >= 92%
	// of grok's context window, force compaction now by returning the
	// Anthropic "prompt is too long" 400, and clear the tracked count so the
	// smaller post-compaction retry forwards (this clear breaks the
	// reject/retry loop).
	if used, window, over := t.overCtx(sessionID); over {
		t.clearCtx(sessionID)
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("prompt is too long: %d tokens > %d maximum", used, window))
		return
	}

	outBody, err := responses.TranslateRequest(body, responses.RequestOpts{
		TargetModel: targetModel,
		SessionID:   sessionID,
		Dialect:     responses.Grok,
	})
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	id := grokRequestIdentity{
		Model:        targetModel,
		SessionID:    sessionID,
		CredentialID: t.opts.CredentialID,
		TurnIdx:      t.turnFor(sessionID, isNewUserTurn(body)),
	}
	resp, err := t.doWith401Retry(r.Context(), outBody, id)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		errBody = decodeBody(resp.Header, errBody)
		if shouldDieFast(errBody, targetModel) {
			t.opts.OnSessionDie(fmt.Sprintf("grok returned model_not_found for %q", targetModel))
		}
		// Reactive backstop: grok's context-length error → the Anthropic
		// "prompt is too long" 400 that Claude Code's reactive-compact path
		// recognizes. Proxy-faithful signal translation.
		if overflow, inTok, maxTok := detectContextOverflow(errBody); overflow {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
				fmt.Sprintf("prompt is too long: %d tokens > %d maximum", inTok, maxTok))
			return
		}
		writeAnthropicError(w, resp.StatusCode, "api_error", string(errBody))
		return
	}

	window, _ := strconv.Atoi(resp.Header.Get("x-grok-context-window"))
	displayModel := sharemw.OriginalModel(r.Context())
	if displayModel == "" {
		displayModel = targetModel
	}
	st := responses.NewStreamTranslator(responses.StreamOpts{
		Model:            displayModel,
		Dialect:          responses.Grok,
		OmitThinkingText: responses.ThinkingOmitted(body),
	})
	src := decodeStream(resp.Header, resp.Body)

	if !bodyStreams(body) {
		// Pre-flush headers before blocking on the full generation so
		// Cloudflare's ~100s no-first-byte timeout is satisfied on tunneled
		// shares (mirrors the codex terminal).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		sse, err := st.Collect(r.Context(), src)
		var js []byte
		if err == nil {
			js, err = responses.AssembleMessage(sse)
		}
		if err != nil {
			// Headers already sent: emit an inline Anthropic-shaped error.
			js, _ = json.Marshal(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "api_error", "message": err.Error()},
			})
		}
		_, _ = w.Write(js)
		t.recordCtx(sessionID, st.FinalUsage().InputTokens, window)
		return
	}

	decision, replay, remaining, err := responses.ClassifyStream(r.Context(), src)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream: "+err.Error())
		return
	}
	if decision.Overflow {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("prompt is too long: %d tokens > %d maximum", decision.InputTokens, decision.Limit))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if err := st.Pipe(r.Context(), io.MultiReader(bytes.NewReader(replay), remaining), w); err != nil && r.Context().Err() == nil {
		// Best-effort error event after a partial stream.
		errBody, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": "stream interrupted: " + err.Error()},
		})
		_, _ = io.WriteString(w, "event: error\ndata: "+string(errBody)+"\n\n")
	}
	t.recordCtx(sessionID, st.FinalUsage().InputTokens, window)
}

// doWith401Retry POSTs body to UpstreamURL/v1/responses with grok-shell's
// identity headers. On a 401 it rebuilds the request (fresh bearer from
// BearerSrc, fresh per-request headers) and retries once.
func (t *Terminal) doWith401Retry(ctx context.Context, body []byte, id grokRequestIdentity) (*http.Response, error) {
	build := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.opts.UpstreamURL+"/v1/responses", bytes.NewReader(body))
		if err != nil {
			return nil, err // unreachable: method + URL are always well-formed
		}
		token, terr := t.opts.BearerSrc.Fresh()
		if terr != nil {
			return nil, terr
		}
		req.Header.Set("Authorization", "Bearer "+token)
		applyGrokIdentity(req, id)
		return req, nil
	}

	req, err := build()
	if err != nil {
		return nil, err
	}
	resp, err := t.opts.Transport.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	req2, err := build()
	if err != nil {
		return nil, err
	}
	return t.opts.Transport.Do(req2)
}

// turnFor returns the session's user-turn index for x-grok-turn-idx. It
// starts at 1 and advances only on a new user turn, so it stays constant
// across tool round-trips within one turn (as grok-shell's does). Sessions
// accumulate for the process lifetime; the map is tiny (one int per Claude
// Code session) so no eviction is needed.
func (t *Terminal) turnFor(sessionID string, newTurn bool) int {
	t.turnMu.Lock()
	defer t.turnMu.Unlock()
	n := t.turns[sessionID]
	if n == 0 || newTurn {
		n++
		t.turns[sessionID] = n
	}
	return n
}

// isNewUserTurn reports whether an inbound Anthropic request starts a new
// user turn: its last non-system message is a user message carrying no
// tool_result block. Claude Code 2.1.286+ may append a role:"system"
// message after the user's, so trailing system messages are skipped.
func isNewUserTurn(body []byte) bool {
	var p struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	for i := len(p.Messages) - 1; i >= 0; i-- {
		m := p.Messages[i]
		if m.Role == "system" {
			continue
		}
		if m.Role != "user" {
			return false
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			return true // string content
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return false
			}
		}
		return true
	}
	return false
}

// overCtx reports whether the last observed prompt size for a session reached
// the proactive-compaction threshold, returning the tracked prompt size and
// window for the error message.
func (t *Terminal) overCtx(sessionID string) (used, window int, over bool) {
	if sessionID == "" {
		return 0, 0, false
	}
	t.ctxMu.Lock()
	defer t.ctxMu.Unlock()
	s := t.ctx[sessionID]
	if s.window > 0 && float64(s.promptTokens) >= ctxCompactThreshold*float64(s.window) {
		return s.promptTokens, s.window, true
	}
	return 0, 0, false
}

// clearCtx forgets a session's tracked usage so the next request forwards
// unconditionally — called right after a proactive trip so the smaller
// post-compaction retry isn't rejected in a loop.
func (t *Terminal) clearCtx(sessionID string) {
	t.ctxMu.Lock()
	defer t.ctxMu.Unlock()
	delete(t.ctx, sessionID)
}

// recordCtx stores grok's reported prompt size + window for a session. No-op
// on missing session id / window / usage (nothing to gate on). Responses
// usage.input_tokens already includes cached tokens.
func (t *Terminal) recordCtx(sessionID string, promptTokens, window int) {
	if sessionID == "" || window <= 0 || promptTokens <= 0 {
		return
	}
	t.ctxMu.Lock()
	defer t.ctxMu.Unlock()
	t.ctx[sessionID] = ctxState{promptTokens: promptTokens, window: window}
}

// bodyStreams reports whether the inbound request asks for a streamed
// response (Anthropic "stream" flag; defaults to true when absent or
// unparseable, matching Claude Code's usual streaming requests).
func bodyStreams(body []byte) bool {
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return true
	}
	return probe.Stream == nil || *probe.Stream
}

// decodeBody returns raw decompressed per the response's Content-Encoding.
// ccm sends grok-shell's authentic Accept-Encoding ("gzip, br, deflate") to
// present as the real client, which disables Go's transparent response
// decompression — so on the error path, where ccm inspects the body
// (die-fast, context-overflow detection), it must inflate the body itself.
// Only the error path uses it; 2xx bodies go through decodeStream
// (decode.go). Unknown/empty encodings and any inflate error fall
// back to raw — best-effort, never worse than forwarding the bytes as-is.
func decodeBody(h http.Header, raw []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Encoding"))) {
	case "", "identity":
		return raw
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return raw
		}
		defer zr.Close()
		if out, err := io.ReadAll(zr); err == nil {
			return out
		}
		return raw
	case "br":
		if out, err := io.ReadAll(brotli.NewReader(bytes.NewReader(raw))); err == nil {
			return out
		}
		return raw
	case "deflate":
		// HTTP "deflate" is nominally zlib-wrapped (RFC 1950); some servers
		// send raw DEFLATE (RFC 1951). Try zlib first, fall back to raw flate.
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			defer zr.Close()
			if out, err := io.ReadAll(zr); err == nil {
				return out
			}
		}
		fr := flate.NewReader(bytes.NewReader(raw))
		defer fr.Close()
		if out, err := io.ReadAll(fr); err == nil {
			return out
		}
		return raw
	default:
		return raw
	}
}

// detectContextOverflow reports whether an upstream error body indicates the
// prompt exceeded grok's context window, and (best-effort) the requested and
// maximum token counts. It recognizes both error shapes xAI/OpenAI use —
// nested {"error":{"message":…}} and flat {"code":…,"error":"…"}} — plus the
// raw body as a last resort. Token counts are extracted heuristically: in an
// overflow the requested count exceeds the limit, so the larger token-like
// integer is the input and the next is the maximum. Counts are advisory;
// Claude Code's reactive compaction triggers on the "prompt is too long"
// message text, and zeros are an acceptable fallback (as in the codex path).
func detectContextOverflow(errBody []byte) (overflow bool, inTokens, maxTokens int) {
	msg := errorMessage(errBody)
	if msg == "" {
		return false, 0, 0
	}
	low := strings.ToLower(msg)
	markers := []string{
		"context length", "context window", "maximum context",
		"context_length_exceeded", "too long", "too many tokens",
		"reduce the length", "exceeds the maximum", "maximum number of tokens",
		// grok/xAI phrasing: "This model's maximum prompt length is 500000
		// but the request contains 500317 tokens." None of the above catch it,
		// so the overflow -> "prompt is too long" translation (which drives
		// Claude Code's auto-compaction) never fired for grok. These do.
		"maximum prompt length", "prompt length", "prompt is too long",
	}
	hit := false
	for _, m := range markers {
		if strings.Contains(low, m) {
			hit = true
			break
		}
	}
	if !hit {
		return false, 0, 0
	}
	nums := tokenLikeInts(msg)
	if len(nums) >= 2 {
		inTokens, maxTokens = nums[0], nums[1]
	}
	return true, inTokens, maxTokens
}

// errorMessage extracts a human-readable message from the two upstream error
// JSON shapes; falls back to the raw body so marker matching still works.
func errorMessage(body []byte) string {
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &nested) == nil && nested.Error.Message != "" {
		return nested.Error.Message
	}
	var flat struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &flat) == nil && flat.Error != "" {
		return flat.Error
	}
	return string(body)
}

// tokenLikeInts returns the integers >= 1000 found in s (token counts are
// large), sorted descending so the requested count precedes the limit.
func tokenLikeInts(s string) []int {
	var out []int
	for i := 0; i < len(s); {
		if s[i] >= '0' && s[i] <= '9' {
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if n, err := strconv.Atoi(s[i:j]); err == nil && n >= 1000 {
				out = append(out, n)
			}
			i = j
		} else {
			i++
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

// writeAnthropicError writes an Anthropic-shaped {"type":"error",...} body.
func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
	_, _ = w.Write(b)
}

// shouldDieFast reports whether errBody (an upstream error response)
// indicates the target model doesn't exist on xAI's side, in which case
// the session should die immediately rather than keep retrying.
func shouldDieFast(errBody []byte, modelOnWire string) bool {
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(errBody, &parsed); err != nil {
		return false
	}
	if parsed.Error.Code == "model_not_found" {
		return true
	}
	if parsed.Error.Type == "invalid_request_error" && modelOnWire != "" &&
		strings.Contains(strings.ToLower(parsed.Error.Message), strings.ToLower(modelOnWire)) {
		return true
	}
	return false
}
