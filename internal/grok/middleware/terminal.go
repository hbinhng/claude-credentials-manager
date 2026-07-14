// Package middleware provides the grok proxy terminal handler.
// cli-chat-proxy.grok.com's /v1/messages is Anthropic-compatible, so this is
// a near-passthrough:
//  1. rewrite the request's model field (alias target when matched, else
//     DefaultModel), preserving JSON key order for prompt-cache prefixes
//  2. swap in the grok OAuth bearer (only ever sent to cli-chat-proxy.grok.com)
//  3. POST to UpstreamURL/v1/messages, presenting as grok-shell
//  4. 401 -> refresh + retry once
//  5. model_not_found -> die-fast
//  6. relay the response (flushing for SSE)
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

	sharemw "github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/trace"
)

const (
	defaultUpstream = "https://cli-chat-proxy.grok.com"
	defaultModel    = "grok-composer-2.5-fast"
)

// ctxCompactThreshold is the fraction of grok's context window at which ccm
// proactively returns "prompt is too long" to force Claude Code compaction
// BEFORE the window is exhausted. The headroom (~8% of ~500k ≈ 40k tokens)
// lets the compaction request itself still fit under grok's hard limit — the
// reactive translation alone fires only once the window is already blown, when
// compaction can no longer fit and the session wedges. Grok reports the window
// via the x-grok-context-window response header and the prompt size via the
// response usage, so this is driven by grok's own counts, not an estimate.
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
	// from upstream, the terminal calls BearerSrc.Fresh() to trigger
	// credflow refresh and retries once with the new token.
	BearerSrc sharemw.BearerSource
	// OnSessionDie is called when a model_not_found error from upstream
	// triggers die-fast. Wired by share.Session to call proxy.Stop.
	OnSessionDie func(reason string)
	// DefaultModel is used when the inbound request's model did not
	// match an alias rule. Defaults to "grok-composer-2.5-fast".
	DefaultModel string
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
// captured from grok's response usage + x-grok-context-window header and used
// by the proactive-compaction gate on the next request.
type ctxState struct {
	promptTokens int
	window       int
}

// NewTerminal constructs a Terminal, filling in defaults for UpstreamURL,
// DefaultModel, OnSessionDie, and Transport when left zero-valued.
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

	// AliasRewrite (the pipeline step upstream of this terminal) already
	// rewrote the body's model field order-preservingly when a rule
	// matched, so this is a no-op on the matched path. On the unmatched
	// path it injects DefaultModel.
	targetModel := t.opts.DefaultModel
	if sharemw.AliasMatched(r.Context()) {
		targetModel = sharemw.EffectiveModel(r.Context())
	}
	outBody := ensureToolRequired(clampEffort(hoistSystemMessages(orderPreservingRewrite(body, targetModel))))

	sessionID := r.Header.Get("X-Claude-Code-Session-Id")

	// Proactive compaction: if the previous turn on this session used >= 92%
	// of grok's context window, force compaction now — before the window is
	// blown — by returning the Anthropic "prompt is too long" 400, and clear
	// the tracked count so the smaller post-compaction retry forwards (this
	// clear is what breaks the reject/retry loop). Grok's real 400 stays the
	// backstop for the first turn of a session and any under-shrink.
	if used, window, over := t.overCtx(sessionID); over {
		t.clearCtx(sessionID)
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("prompt is too long: %d tokens > %d maximum", used, window))
		return
	}

	resp, err := t.doWith401Retry(r.Context(), outBody, targetModel, sessionID, bodyStreams(outBody))
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
		// Grok's context window is smaller than the Claude model Claude Code
		// thinks it is talking to, so a long conversation overflows before
		// Claude Code proactively compacts. xAI rejects it with a
		// context-length error; translate that into the Anthropic-shape
		// "prompt is too long" 400 that Claude Code's reactive-compact path
		// recognizes (mirrors the codex overflow translation). Proxy-faithful
		// signal translation — no harness env-var poking.
		if overflow, inTok, maxTok := detectContextOverflow(errBody); overflow {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
				fmt.Sprintf("prompt is too long: %d tokens > %d maximum", inTok, maxTok))
			return
		}
		writeAnthropicError(w, resp.StatusCode, "api_error", string(errBody))
		return
	}

	// 2xx: relay headers + body, flushing per-write for SSE.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	// Tee the head of the response so we can read grok's reported prompt-token
	// usage (message_start event for SSE, or the JSON body) for the next-turn
	// proactive-compaction gate. Capped — the usage lands in the first event.
	var head bytes.Buffer
	const headCap = 16 << 10
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
			if head.Len() < headCap {
				if grow := headCap - head.Len(); n <= grow {
					head.Write(buf[:n])
				} else {
					head.Write(buf[:grow])
				}
			}
		}
		if rerr != nil {
			if pt, ok := extractPromptTokens(head.Bytes()); ok {
				window, _ := strconv.Atoi(resp.Header.Get("x-grok-context-window"))
				t.recordCtx(sessionID, pt, window)
			}
			break
		}
	}
}

// doWith401Retry POSTs body to UpstreamURL/v1/messages with grok-shell's
// identity headers. On a 401 it calls BearerSrc.Fresh() and retries once with
// the rotated token (and fresh per-request identity headers).
func (t *Terminal) doWith401Retry(ctx context.Context, body []byte, model, sessionID string, stream bool) (*http.Response, error) {
	build := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.opts.UpstreamURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err // unreachable: method + URL are always well-formed
		}
		token, terr := t.opts.BearerSrc.Fresh()
		if terr != nil {
			return nil, terr
		}
		req.Header.Set("Authorization", "Bearer "+token)
		applyGrokIdentity(req, model, sessionID, t.nextTurn(sessionID), stream)
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

// nextTurn returns a monotonic per-session request counter (1-based), used for
// x-grok-turn-idx. Sessions accumulate for the process lifetime; the map is
// tiny (one int per Claude Code session) so no eviction is needed.
func (t *Terminal) nextTurn(sessionID string) int {
	t.turnMu.Lock()
	defer t.turnMu.Unlock()
	t.turns[sessionID]++
	return t.turns[sessionID]
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
// on missing session id / window / usage (nothing to gate on).
func (t *Terminal) recordCtx(sessionID string, promptTokens, window int) {
	if sessionID == "" || window <= 0 || promptTokens <= 0 {
		return
	}
	t.ctxMu.Lock()
	defer t.ctxMu.Unlock()
	t.ctx[sessionID] = ctxState{promptTokens: promptTokens, window: window}
}

// extractPromptTokens finds grok's reported input-side prompt size in a
// response head: the first "usage" object carrying input_tokens (Anthropic's
// message_start event for a streamed response, or the top-level usage of a
// non-streamed JSON body). Returns input_tokens + cache_read + cache_creation
// — the full prompt the model actually saw. The usage object is flat, so the
// first "}" closes it.
func extractPromptTokens(b []byte) (int, bool) {
	needle := []byte(`"usage"`)
	for i := bytes.Index(b, needle); i >= 0; {
		rest := b[i+len(needle):]
		open := bytes.IndexByte(rest, '{')
		if open < 0 {
			return 0, false
		}
		end := bytes.IndexByte(rest[open:], '}')
		if end < 0 {
			return 0, false
		}
		var u struct {
			InputTokens   *int `json:"input_tokens"`
			CacheRead     int  `json:"cache_read_input_tokens"`
			CacheCreation int  `json:"cache_creation_input_tokens"`
		}
		if json.Unmarshal(rest[open:open+end+1], &u) == nil && u.InputTokens != nil {
			return *u.InputTokens + u.CacheRead + u.CacheCreation, true
		}
		adv := i + len(needle) + open + end + 1
		next := bytes.Index(b[adv:], needle)
		if next < 0 {
			return 0, false
		}
		i = adv + next
	}
	return 0, false
}

// bodyStreams reports whether the request asks for a streamed response
// (Anthropic "stream" flag; defaults to true when absent/unparseable so the
// Accept header matches Claude Code's usual streaming requests).
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
// The 2xx relay path is a faithful header+body passthrough and needs no
// decoding (it forwards Content-Encoding alongside the raw bytes and the end
// client decompresses). Unknown/empty encodings and any inflate error fall
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

// orderPreservingRewrite does a best-effort textual rewrite of the body's
// "model" value, splicing in place rather than unmarshal/remarshal, so
// unrelated key order is preserved (prompt-cache prefix discipline; see
// rewriteModelField in internal/share/middleware/alias.go, which this
// mirrors). Falls back to returning body unchanged when the "model" key
// isn't present in the expected shape.
// clampEffort maps output_config.effort "xhigh" down to "high". Claude Code
// emits four effort levels (low/medium/high/xhigh) but grok's scale is only
// low/medium/high, so xhigh would be rejected. Best-effort and cheap: skips
// unless the literal "xhigh" is present, and only re-marshals when it is
// actually the effort value.
func clampEffort(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"xhigh"`)) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	oc, ok := m["output_config"].(map[string]any)
	if !ok {
		return body
	}
	if eff, _ := oc["effort"].(string); eff == "xhigh" {
		oc["effort"] = "high"
		if out, err := json.Marshal(m); err == nil {
			return out
		}
	}
	return body
}

// hoistSystemMessages moves any role:"system" message out of the messages
// array and into the top-level `system` field. Claude Code injects
// SessionStart-hook / skill context as a role:"system" message, which the
// real Anthropic API tolerates but xAI's /v1/messages validator rejects
// ("Invalid message role" — it only accepts user/assistant). Hoisting keeps
// the content as a system instruction (its correct Anthropic home) and
// yields a request xAI accepts.
//
// Best-effort: a body with no role:"system" message, or one that doesn't
// parse, is returned unchanged (no re-marshal).
func hoistSystemMessages(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"role":"system"`)) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	msgs, ok := m["messages"].([]any)
	if !ok {
		return body
	}
	var hoisted []any
	kept := make([]any, 0, len(msgs))
	for _, raw := range msgs {
		if msg, ok := raw.(map[string]any); ok {
			if role, _ := msg["role"].(string); role == "system" {
				hoisted = append(hoisted, systemContentBlocks(msg["content"])...)
				continue
			}
		}
		kept = append(kept, raw)
	}
	if len(hoisted) == 0 {
		return body
	}
	m["messages"] = kept
	m["system"] = mergeSystem(m["system"], hoisted)
	out, err := json.Marshal(m)
	if err != nil {
		// untestable: a map decoded from valid JSON always re-marshals.
		return body
	}
	return out
}

// systemContentBlocks normalizes a message's content (string or []block)
// into a slice of Anthropic content blocks.
func systemContentBlocks(content any) []any {
	switch c := content.(type) {
	case string:
		return []any{map[string]any{"type": "text", "text": c}}
	case []any:
		return c
	default:
		return nil
	}
}

// mergeSystem appends blocks to an existing top-level system value,
// normalizing it to a content-block array (string → one text block;
// nil → just the new blocks).
func mergeSystem(sys any, blocks []any) []any {
	var out []any
	switch s := sys.(type) {
	case string:
		if s != "" {
			out = append(out, map[string]any{"type": "text", "text": s})
		}
	case []any:
		out = append(out, s...)
	}
	return append(out, blocks...)
}

// ensureToolRequired makes every tool's input_schema carry a `required`
// array. xAI's /v1/messages validator rejects a tool schema whose
// `required` is absent or null ("/required: null is not of type array"),
// whereas Anthropic treats it as optional — so Claude Code omits it for
// tools with no required parameters. Injecting an empty array is
// semantically identical (no required params) and unblocks the request.
//
// Best-effort and order-preserving-friendly: a body with no tool schemas,
// one whose tools already all carry `required`, or one that doesn't parse
// as JSON is returned unchanged (no re-marshal, so the order-preserving
// model rewrite upstream is untouched). Only when a fix is actually needed
// is the body re-serialized.
func ensureToolRequired(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"input_schema"`)) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	tools, ok := m["tools"].([]any)
	if !ok {
		return body
	}
	changed := false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		schema, ok := tool["input_schema"].(map[string]any)
		if !ok {
			continue
		}
		if v, has := schema["required"]; !has || v == nil {
			schema["required"] = []any{}
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		// untestable: a map decoded from valid JSON always re-marshals.
		return body
	}
	return out
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

func orderPreservingRewrite(body []byte, model string) []byte {
	const key = `"model"`
	idx := bytes.Index(body, []byte(key))
	if idx < 0 {
		return body
	}
	rest := body[idx+len(key):]
	colonIdx := bytes.IndexByte(rest, ':')
	if colonIdx < 0 {
		return body
	}
	rest = rest[colonIdx+1:]
	q1 := bytes.IndexByte(rest, '"')
	if q1 < 0 {
		return body
	}
	rest = rest[q1+1:]
	q2 := bytes.IndexByte(rest, '"')
	if q2 < 0 {
		return body
	}
	prefix := body[:len(body)-len(rest)] // up to and including opening quote
	suffix := rest[q2:]                  // closing quote + rest
	return append(append([]byte{}, prefix...), append([]byte(model), suffix...)...)
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
