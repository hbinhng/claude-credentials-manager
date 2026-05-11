package translator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// StreamDecision is the outcome of ClassifyStream: either the upstream
// is producing content normally (stream as SSE) or the upstream is
// signalling a context-overflow condition (no actionable delta before
// response.incomplete{max_output_tokens}) that should be translated
// into an HTTP 400 prompt-too-long error.
type StreamDecision struct {
	Overflow    bool // true iff response.incomplete{max_output_tokens} arrived without actionable content
	InputTokens int  // populated only when Overflow; from upstream usage.input_tokens
	Limit       int  // populated only when Overflow; usage.input_tokens + usage.output_tokens
}

// classifyFirstByteTimeout caps how long ClassifyStream waits for a
// decisive event before falling through to streaming mode. In practice
// chatgpt.com emits in_progress within ~2s and a decisive event within
// ~10s; 30s gives generous margin while staying well under Cloudflare's
// ~100s no-first-byte timeout for tunneled shares. Exposed as a var so
// tests can lower it (same pattern as collectMaxBytes in stream.go).
var classifyFirstByteTimeout = 30 * time.Second

// ClassifyStream reads upstream SSE bytes from src until it can decide
// whether to stream as normal (visible content arrived) or short-circuit
// to a 400 prompt-too-long (response.incomplete{max_output_tokens}
// arrived without any actionable delta). Returns the decision, the
// bytes already consumed (caller replays these through the translator
// when streaming), and a reader for the remaining upstream bytes
// (caller chains this after replay).
//
// On timeout or EOF returns a non-overflow decision with nil error so
// the caller can fall through to streaming; the translator's EOF
// safety net handles any abnormal termination from there.
func ClassifyStream(ctx context.Context, src io.Reader) (StreamDecision, []byte, io.Reader, error) {
	br := bufio.NewReader(src)
	var replay []byte

	deadline := time.Now().Add(classifyFirstByteTimeout)

	for {
		if err := ctx.Err(); err != nil {
			return StreamDecision{}, replay, br, err
		}
		if time.Now().After(deadline) {
			return StreamDecision{}, replay, br, nil
		}

		line, err := br.ReadString('\n')
		if len(line) > 0 {
			replay = append(replay, line...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return StreamDecision{}, replay, br, nil
			}
			return StreamDecision{}, replay, br, err
		}

		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		payload = strings.TrimRight(payload, "\r\n")
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var ev codexEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "response.output_text.delta",
			"response.function_call_arguments.delta":
			return StreamDecision{}, replay, br, nil

		case "response.completed", "response.failed":
			return StreamDecision{}, replay, br, nil

		case "response.incomplete":
			reason := ""
			var usage *codexUsage
			if ev.Response != nil {
				if ev.Response.IncompleteDetails != nil {
					reason = ev.Response.IncompleteDetails.Reason
				}
				usage = ev.Response.Usage
			}
			if reason == "max_output_tokens" {
				dec := StreamDecision{Overflow: true}
				if usage != nil {
					dec.InputTokens = usage.InputTokens
					dec.Limit = usage.InputTokens + usage.OutputTokens
				}
				return dec, replay, br, nil
			}
			return StreamDecision{}, replay, br, nil
		}
		// All other event types (response.created, in_progress,
		// output_item.added/done, content_part.*, reasoning_*part.added,
		// reasoning_summary_text.delta, reasoning_text.delta, etc.) are
		// non-decisive — buffer and continue.
	}
}
