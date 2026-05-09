// Package middleware provides the codex-specific pipeline terminal
// handler. It composes:
//  1. AliasMatched? translate request body : pass through
//  2. Apply identity bundle (captured headers + cred bearer)
//  3. POST to upstream /v1/responses via bogdanfinn
//  4. SSE reshape on the response (translator.StreamTranslator)
//  5. Die-fast detection on model_not_found errors
//  6. 401 → refresh + retry once
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/capture"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/identity"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/transport"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/translator"
	sharemw "github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// TerminalOpts configures the codex terminal handler.
type TerminalOpts struct {
	Cred        *store.Credential
	Transport   *transport.Transport
	Capture     *capture.Result // produced by capture.Run at session start
	Bundle      *identity.Bundle
	// UpstreamURL overrides the codex backend (default "https://chatgpt.com").
	// Test-only; production callers leave it blank.
	UpstreamURL string
	// BearerSrc fetches the cred's current access token. Same interface
	// the rest of the share pipeline uses (typically a *credState).
	// On a 401 from upstream, the terminal calls BearerSrc.Fresh() to
	// trigger credflow refresh and retries once with the new token.
	BearerSrc sharemw.BearerSource
	// OnSessionDie is called when a model_not_found error from upstream
	// triggers die-fast. Wired by share.Session to call proxy.Stop.
	OnSessionDie func(reason string)
	// QuotaCache, if non-nil, is called after each upstream response to
	// parse x-codex-{5h,7d}-* headers and update per-cred usage telemetry.
	QuotaCache *QuotaCache
	// UsageTee, if non-nil, records a UsageEvent after each translated
	// response (Pipe or Collect path) for display by ccm status / ccm serve.
	UsageTee *UsageTee
}

// Terminal is the codex-specific http.Handler that lives at the end of
// the share pipeline.
type Terminal struct {
	opts TerminalOpts
}

// NewTerminal constructs a Terminal. Defaults UpstreamURL to
// "https://chatgpt.com" and OnSessionDie to a no-op if not provided.
func NewTerminal(opts TerminalOpts) *Terminal {
	if opts.UpstreamURL == "" {
		opts.UpstreamURL = "https://chatgpt.com"
	}
	if opts.OnSessionDie == nil {
		opts.OnSessionDie = func(string) {} // no-op default
	}
	return &Terminal{opts: opts}
}

// ServeHTTP implements http.Handler.
func (t *Terminal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	matched := sharemw.AliasMatched(r.Context())
	effectiveModel := sharemw.EffectiveModel(r.Context())
	originalModel := sharemw.OriginalModel(r.Context())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "read body: "+err.Error())
		return
	}

	var outBody []byte
	if matched {
		// Translate Anthropic Messages → OpenAI Responses. Per spec
		// 2026-05-09-codex-omniroute-pivot §5.2 InstallationID and
		// PromptCacheKey were removed from RequestOpts. ServiceTier
		// passthrough from Capture is preserved here pending Task 4
		// (which removes the Capture field entirely).
		reqOpts := translator.RequestOpts{
			TargetModel: effectiveModel,
			ServiceTier: t.opts.Capture.ServiceTier,
		}
		outBody, err = translator.TranslateRequest(body, reqOpts)
		if err != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	} else {
		// Pass-through: forward original body unchanged. Codex will
		// likely return model_not_found which triggers die-fast below.
		outBody = body
	}

	resp, err := t.doWith401Retry(r.Context(), outBody)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Parse quota headers regardless of response status.
	if t.opts.QuotaCache != nil {
		t.opts.QuotaCache.Apply(resp)
	}

	// Non-2xx: parse error body, check for die-fast trigger.
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		modelOnWire := effectiveModel
		if !matched {
			modelOnWire = originalModel
		}
		if shouldDieFast(errBody, modelOnWire) {
			t.opts.OnSessionDie(fmt.Sprintf(
				"codex returned model_not_found for %q (alias matched=%v, original=%q)",
				modelOnWire, matched, originalModel))
		}
		writeAnthropicError(w, resp.StatusCode, "api_error", string(errBody))
		return
	}

	// 2xx: matched → SSE reshape; passthrough → raw stream.
	if !matched {
		// Pass-through SSE: copy upstream headers and stream raw bytes.
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.Copy(w, resp.Body)
		return
	}

	st := translator.NewStreamTranslator(translator.StreamOpts{
		MessageID: resp.Header.Get("X-Response-Id"),
		Model:     originalModel, // surface inbound model name to the client
	})

	// stream:false → buffer entire response and return JSON.
	if isStreamFalse(body) {
		js, err := st.Collect(r.Context(), resp.Body)
		if err != nil {
			writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
			return
		}
		if t.opts.UsageTee != nil {
			fu := st.FinalUsage()
			t.opts.UsageTee.Record(UsageEvent{
				InputTokens:          fu.InputTokens,
				OutputTokens:         fu.OutputTokens,
				CacheReadInputTokens: fu.CacheReadInputTokens,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(js)
		return
	}

	// Streaming SSE path.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if err := st.Pipe(r.Context(), resp.Body, w); err != nil && r.Context().Err() == nil {
		// Best-effort error event after partial stream.
		errBody := `{"type":"error","error":{"type":"api_error","message":"stream interrupted: ` + err.Error() + `"}}`
		_, _ = io.WriteString(w, "event: error\ndata: "+errBody+"\n\n")
	}
	if t.opts.UsageTee != nil {
		fu := st.FinalUsage()
		t.opts.UsageTee.Record(UsageEvent{
			InputTokens:          fu.InputTokens,
			OutputTokens:         fu.OutputTokens,
			CacheReadInputTokens: fu.CacheReadInputTokens,
		})
	}
}

// doWith401Retry POSTs body to /v1/responses; if the upstream returns
// 401, it triggers a credflow refresh via BearerSrc.Fresh() and retries
// once with the new bearer. Per spec §10.
func (t *Terminal) doWith401Retry(ctx context.Context, body []byte) (*http.Response, error) {
	build := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST",
			t.opts.UpstreamURL+"/v1/responses", bytes.NewReader(body))
		if err != nil {
			// Unreachable in production: "POST" is a valid method and
			// UpstreamURL is validated to be non-empty by NewTerminal.
			// http.NewRequestWithContext only fails on invalid method or
			// malformed URL; neither applies here.
			return nil, err
		}
		t.opts.Bundle.Apply(req)
		return req, nil
	}
	req, err := build()
	if err != nil {
		// Unreachable: see comment in build() above.
		return nil, err
	}
	resp, err := t.opts.Transport.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || t.opts.BearerSrc == nil {
		return resp, nil
	}
	// 401: drain + close, refresh credential, retry once.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if _, err := t.opts.BearerSrc.Fresh(); err != nil {
		return nil, fmt.Errorf("upstream 401 and refresh failed: %w", err)
	}
	req2, err := build()
	if err != nil {
		// Unreachable: see comment in build() above. The URL and method
		// have not changed between the first and second call.
		return nil, err
	}
	return t.opts.Transport.Do(req2)
}

// writeAnthropicError writes a JSON Anthropic-style error to w.
func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
	_, _ = w.Write(body)
}

// shouldDieFast parses an upstream error body and reports whether it
// indicates "model not found" for the model on the wire. Per spec §10.
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
	if parsed.Error.Type == "invalid_request_error" {
		if modelOnWire != "" && strings.Contains(
			strings.ToLower(parsed.Error.Message),
			strings.ToLower(modelOnWire)) {
			return true
		}
	}
	return false
}

// isStreamFalse peeks the request body for "stream":false.
func isStreamFalse(body []byte) bool {
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		// Unreachable in production: isStreamFalse is only called after
		// TranslateRequest succeeded (matched path) or after AliasRewrite
		// accepted the JSON body (passthrough path). Both guarantee
		// body is valid JSON.
		return false
	}
	return probe.Stream != nil && !*probe.Stream
}

// derivePromptCacheKey returns the X-Claude-Code-Session-Id header if
// present, otherwise falls back to the captured session_id.
func derivePromptCacheKey(r *http.Request, fallback string) string {
	if v := r.Header.Get("X-Claude-Code-Session-Id"); v != "" {
		return v
	}
	return fallback
}
