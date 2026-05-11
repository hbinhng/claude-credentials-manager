package translator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// StreamOpts configures a StreamTranslator.
type StreamOpts struct {
	MessageID string // synthesized; usually mirrors codex response.id
	Model     string // post-alias model name (for message_start)
}

// StreamTranslator consumes codex SSE events and emits Anthropic SSE
// events. Stateful (tracks open content blocks and the next index).
type StreamTranslator struct {
	opts            StreamOpts
	nextBlockIndex  int
	currentBlockIdx int    // -1 when no block open
	currentType     string // type of the currently open block ("message"|"reasoning"|"function_call")
	lastClosedType  string // type of the most recently closed block; used by mapStopReason
	messageStarted  bool
	messageEnded    bool
	stopReason      string
	usage           *anthropicUsage

	// WORKAROUND: codex models emit Read({pages:""}) on non-PDF reads;
	// Claude Code's validator rejects with "Invalid pages parameter"
	// and the model retries without the field. Suppress the retry by
	// buffering Read args until .done, then dropping an empty pages
	// key before emitting. Scoped to Read only — every other tool
	// continues to stream deltas verbatim.
	bufferReadArgs bool
	readArgBuf     strings.Builder
}

type anthropicUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens,omitempty"`
}

// NewStreamTranslator constructs a StreamTranslator.
func NewStreamTranslator(opts StreamOpts) *StreamTranslator {
	return &StreamTranslator{opts: opts, currentBlockIdx: -1}
}

// Pipe drives the translation loop end-to-end.
func (t *StreamTranslator) Pipe(ctx context.Context, src io.Reader, dst io.Writer) error {
	return t.run(ctx, src, dst)
}

// collectMaxBytes is the maximum number of bytes Collect will buffer before
// returning an error. Exposed as a var so tests can lower the threshold without
// allocating 64 MiB; must not be changed outside of test code.
var collectMaxBytes = 64 << 20 // 64 MiB

// Collect runs translation in buffer mode. Returns all Anthropic SSE
// bytes accumulated from the stream. Hard cap: collectMaxBytes total content.
func (t *StreamTranslator) Collect(ctx context.Context, src io.Reader) ([]byte, error) {
	buf := &bytes.Buffer{}
	if err := t.run(ctx, src, buf); err != nil {
		return nil, err
	}
	if buf.Len() > collectMaxBytes {
		return jsonError("api_error", fmt.Sprintf("response exceeded %d MiB buffer cap; use stream:true", collectMaxBytes>>20))
	}
	return buf.Bytes(), nil
}

// FinalUsageInfo is the usage block from the last response.completed
// event. Defined in translator (not middleware) to avoid an import
// cycle. The codex middleware Terminal converts it to its own
// middleware.UsageEvent shape.
type FinalUsageInfo struct {
	InputTokens          int
	OutputTokens         int
	CacheReadInputTokens int
}

// FinalUsage returns the last-seen usage block, or zero values if no
// response.completed event was seen.
func (t *StreamTranslator) FinalUsage() FinalUsageInfo {
	if t.usage == nil {
		return FinalUsageInfo{}
	}
	return FinalUsageInfo{
		InputTokens:          t.usage.InputTokens,
		OutputTokens:         t.usage.OutputTokens,
		CacheReadInputTokens: t.usage.CacheReadInputTokens,
	}
}

// run is the shared event-by-event reducer.
func (t *StreamTranslator) run(ctx context.Context, src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var ev codexEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue // tolerant: skip malformed lines
		}
		emissions := t.apply(ev)
		for _, em := range emissions {
			if err := writeSSE(dst, em.name, em.data); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

type codexEvent struct {
	Type     string             `json:"type"`
	Item     *codexOutputItem   `json:"item,omitempty"`
	Delta    string             `json:"delta,omitempty"`
	Status   string             `json:"status,omitempty"` // for response.completed
	Index    int                `json:"output_index,omitempty"`
	Response *codexResponseInfo `json:"response,omitempty"` // for response.created and response.completed
}

type codexResponseInfo struct {
	ID                string                  `json:"id"`
	Usage             *codexUsage             `json:"usage,omitempty"`              // populated on response.completed / .incomplete / .failed
	Error             *codexResponseError     `json:"error,omitempty"`              // populated on response.failed
	IncompleteDetails *codexIncompleteDetails `json:"incomplete_details,omitempty"` // populated on response.incomplete
}

type codexResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type codexIncompleteDetails struct {
	Reason string `json:"reason"`
}

type codexOutputItem struct {
	Type   string `json:"type"` // "message" | "reasoning" | "function_call"
	ID     string `json:"id"`
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

type codexUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
}

type emission struct {
	name string
	data string
}

func (t *StreamTranslator) apply(ev codexEvent) []emission {
	switch ev.Type {
	case "response.created":
		if t.messageStarted {
			return nil
		}
		t.messageStarted = true
		msgID := t.opts.MessageID
		if ev.Response != nil && ev.Response.ID != "" {
			// Translate chatgpt.com's resp_<id> to Anthropic-shape msg_<id>.
			// Strip the resp_ prefix if present so we don't double-prefix.
			upstreamID := strings.TrimPrefix(ev.Response.ID, "resp_")
			msgID = "msg_" + upstreamID
		}
		body, _ := json.Marshal(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":          msgID,
				"type":        "message",
				"role":        "assistant",
				"model":       t.opts.Model,
				"content":     []any{},
				"stop_reason": nil,
				"usage":       map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		})
		return []emission{{name: "message_start", data: string(body)}}

	case "response.in_progress":
		return nil

	case "response.output_item.added":
		return t.openBlock(ev)

	case "response.output_text.delta":
		body, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": t.currentBlockIdx,
			"delta": map[string]any{"type": "text_delta", "text": ev.Delta},
		})
		return []emission{{name: "content_block_delta", data: string(body)}}

	case "response.output_text.done":
		return t.closeBlock()

	case "response.reasoning_summary_text.delta":
		body, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": t.currentBlockIdx,
			"delta": map[string]any{"type": "thinking_delta", "thinking": ev.Delta},
		})
		return []emission{{name: "content_block_delta", data: string(body)}}

	case "response.reasoning_summary_text.done":
		return t.closeBlock()

	case "response.reasoning_text.delta":
		// Forward-compat: codex-rs distinguishes reasoning_text from
		// reasoning_summary_text in its vocabulary, but chatgpt.com's
		// chat endpoint only emits the summary variant today. Mirror
		// the summary handler so we don't drop reasoning if the
		// upstream protocol ever shifts. Guard on currentType so a
		// stray delta outside a reasoning block can't corrupt a
		// message/tool block. Block closure is handled by
		// response.output_item.done (the defensive close added in
		// the same fix).
		if t.currentType != "reasoning" {
			return nil
		}
		body, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": t.currentBlockIdx,
			"delta": map[string]any{"type": "thinking_delta", "thinking": ev.Delta},
		})
		return []emission{{name: "content_block_delta", data: string(body)}}

	case "response.function_call_arguments.delta":
		if t.bufferReadArgs {
			t.readArgBuf.WriteString(ev.Delta)
			return nil
		}
		body, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": t.currentBlockIdx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": ev.Delta},
		})
		return []emission{{name: "content_block_delta", data: string(body)}}

	case "response.function_call_arguments.done":
		if t.bufferReadArgs {
			t.bufferReadArgs = false
			partial := dropEmptyReadPages(t.readArgBuf.String())
			t.readArgBuf.Reset()
			body, _ := json.Marshal(map[string]any{
				"type":  "content_block_delta",
				"index": t.currentBlockIdx,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
			})
			return append([]emission{{name: "content_block_delta", data: string(body)}}, t.closeBlock()...)
		}
		return t.closeBlock()

	case "response.completed":
		blockType := t.currentType
		if blockType == "" {
			blockType = t.lastClosedType
		}
		stop := mapStopReason(ev.Status, blockType)
		var usage *codexUsage
		if ev.Response != nil {
			usage = ev.Response.Usage
		}
		return t.finalize(stop, usage)

	case "response.failed":
		var errCode, errMsg string
		var usage *codexUsage
		if ev.Response != nil {
			if ev.Response.Error != nil {
				errCode = ev.Response.Error.Code
				errMsg = ev.Response.Error.Message
			}
			usage = ev.Response.Usage
		}
		if errMsg == "" {
			errMsg = "codex upstream returned response.failed"
		}
		errBody, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": mapFailedCode(errCode), "message": errMsg},
		})
		em := []emission{{name: "error", data: string(errBody)}}
		return append(em, t.finalize("end_turn", usage)...)

	case "response.incomplete":
		reason := ""
		var usage *codexUsage
		if ev.Response != nil {
			if ev.Response.IncompleteDetails != nil {
				reason = ev.Response.IncompleteDetails.Reason
			}
			usage = ev.Response.Usage
		}
		return t.finalize(mapIncompleteReason(reason), usage)

	case "response.output_item.done":
		// Defensive close. When the inner _text.done / _arguments.done
		// already closed the block this is a no-op (closeBlock returns
		// nil when currentBlockIdx < 0). When the item has no inner
		// content (e.g. an empty reasoning summary: []), this is the
		// only event that closes the block — without it the stream
		// ends with an open content block and Claude Code rejects it
		// as "API returned an empty or malformed response (HTTP 200)".
		return t.closeBlock()

	// Dropped event types:
	// response.content_part.added           — parent output_item.added already opened the block
	// response.content_part.done            — closed by inner _text.done / _arguments.done; output_item.done is the safety net
	// response.reasoning_summary_part.added — parent output_item.added already opened the block
	}
	return nil
}

// openBlock translates a codex `response.output_item.added` event
// into a single Anthropic `content_block_start`. Codex's protocol
// (codex-rs/protocol/src/models.rs ResponseItem::Message) carries
// `content: Vec<ContentItem>` — meaning a single Message item could
// in theory hold multiple blocks. In practice, chatgpt.com always
// emits one block per output_item.added event (verified against
// codex-rs/core/tests/common/responses.rs), so we open exactly one
// block per event. If chatgpt.com ever starts emitting multi-block
// messages we'd see codex's `content_index` field on delta events
// (codex-rs/protocol/src/protocol.rs:1858) carrying non-zero values
// — that's the signal to revisit this assumption.
func (t *StreamTranslator) openBlock(ev codexEvent) []emission {
	if ev.Item == nil {
		return nil
	}
	idx := t.nextBlockIndex
	t.nextBlockIndex++
	t.currentBlockIdx = idx
	t.currentType = ev.Item.Type

	var content map[string]any
	switch ev.Item.Type {
	case "message":
		content = map[string]any{"type": "text", "text": ""}
	case "reasoning":
		content = map[string]any{"type": "thinking", "thinking": ""}
	case "function_call":
		if ev.Item.Name == "Read" {
			t.bufferReadArgs = true
			t.readArgBuf.Reset()
		}
		content = map[string]any{
			"type":  "tool_use",
			"id":    ev.Item.CallID,
			"name":  ev.Item.Name,
			"input": map[string]any{},
		}
	default:
		// Unknown item type; undo index increment and bail.
		t.nextBlockIndex--
		t.currentBlockIdx = -1
		t.currentType = ""
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": content,
	})
	return []emission{{name: "content_block_start", data: string(body)}}
}

func (t *StreamTranslator) closeBlock() []emission {
	if t.currentBlockIdx < 0 {
		return nil
	}
	idx := t.currentBlockIdx
	t.lastClosedType = t.currentType
	t.currentBlockIdx = -1
	t.currentType = ""
	body, _ := json.Marshal(map[string]any{
		"type":  "content_block_stop",
		"index": idx,
	})
	return []emission{{name: "content_block_stop", data: string(body)}}
}

// finalize centralizes the close sequence for terminal events.
// Closes any open content block, emits message_delta with the given
// stop_reason+usage, then emits message_stop and marks the message
// ended. Idempotent: returns nil if the message is already ended.
//
// Every terminal handler (response.completed, response.incomplete,
// response.failed) and the run() EOF safety net go through this so
// the SSE stream is guaranteed to terminate cleanly with a
// message_stop event.
func (t *StreamTranslator) finalize(stopReason string, usage *codexUsage) []emission {
	if t.messageEnded {
		return nil
	}
	em := t.closeBlock()
	em = append(em, t.flushMessageDeltaWithStop(stopReason, usage)...)
	em = append(em, emission{name: "message_stop", data: `{"type":"message_stop"}`})
	t.messageEnded = true
	return em
}

// flushMessageDeltaWithStop emits a message_delta with an explicit
// stop_reason and usage. Called by finalize() — the central
// close-the-stream helper. Kept as a separate function (rather than
// inlining into finalize) so that the message_delta JSON shape is
// localized to one place, even though the only caller is finalize.
func (t *StreamTranslator) flushMessageDeltaWithStop(stopReason string, usage *codexUsage) []emission {
	usageOut := map[string]any{
		"input_tokens":  0,
		"output_tokens": 0,
	}
	if usage != nil {
		usageOut["input_tokens"] = usage.InputTokens
		usageOut["output_tokens"] = usage.OutputTokens
		cachedRead := 0
		if usage.InputTokensDetails != nil {
			cachedRead = usage.InputTokensDetails.CachedTokens
		}
		if cachedRead > 0 {
			usageOut["cache_read_input_tokens"] = cachedRead
		}
		t.usage = &anthropicUsage{
			InputTokens:          usage.InputTokens,
			OutputTokens:         usage.OutputTokens,
			CacheReadInputTokens: cachedRead,
		}
	}
	body, _ := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": usageOut,
	})
	return []emission{{name: "message_delta", data: string(body)}}
}

// dropEmptyReadPages parses argsJSON as a JSON object and returns a
// re-encoded form with the "pages" key removed when its value is the
// empty string. Returns argsJSON unchanged on parse failure or when
// no transformation is needed. See the bufferReadArgs comment on
// StreamTranslator for the rationale.
func dropEmptyReadPages(argsJSON string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}
	v, present := args["pages"]
	if !present {
		return argsJSON
	}
	s, isString := v.(string)
	if !isString || s != "" {
		return argsJSON
	}
	delete(args, "pages")
	out, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	return string(out)
}

func mapStopReason(status, lastBlockType string) string {
	if status == "length" {
		return "max_tokens"
	}
	if lastBlockType == "function_call" {
		return "tool_use"
	}
	return "end_turn"
}

// mapIncompleteReason translates the chatgpt.com response.incomplete
// "incomplete_details.reason" string into an Anthropic stop_reason.
// Unknown / empty reasons collapse to "end_turn" — staying inside
// Anthropic's stop_reason enum is required for Claude Code to render
// the partial assistant message.
func mapIncompleteReason(reason string) string {
	switch reason {
	case "max_output_tokens":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	}
	return "end_turn"
}

// mapFailedCode translates the chatgpt.com response.failed
// "error.code" string into an Anthropic-shape error.type. Unknown
// codes collapse to "api_error". The upstream error.message is
// passed through verbatim by callers — only the type is mapped.
func mapFailedCode(code string) string {
	switch code {
	case "context_length_exceeded":
		return "request_too_large"
	case "insufficient_quota", "rate_limit_exceeded":
		return "rate_limit_error"
	case "server_overloaded":
		return "overloaded_error"
	case "invalid_prompt", "cyber_policy":
		return "invalid_request_error"
	}
	return "api_error"
}

func writeSSE(w io.Writer, name, data string) error {
	if _, err := io.WriteString(w, "event: "+name+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: "+data+"\n\n"); err != nil {
		return err
	}
	if f, ok := w.(interface{ Flush() }); ok {
		f.Flush()
	}
	return nil
}

func jsonError(typ, msg string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": msg},
	})
}
