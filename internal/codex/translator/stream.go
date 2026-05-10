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
	opts                StreamOpts
	nextBlockIndex      int
	currentBlockIdx     int    // -1 when no block open
	currentType         string // type of the currently open block ("message"|"reasoning"|"function_call")
	lastClosedType      string // type of the most recently closed block; used by mapStopReason
	messageStarted      bool
	messageEnded        bool
	stopReason          string
	usage               *anthropicUsage
	currentToolRenameTo    string          // codex tool name when current block is a renamed tool_use; "" otherwise
	currentBlockClaudeName string          // Claude tool name (Read, Glob, ...) for the open function_call block
	argBuffer              strings.Builder // accumulated function_call_arguments deltas for renamed tools
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
	Usage    *codexUsage        `json:"usage,omitempty"`
	Status   string             `json:"status,omitempty"` // for response.completed
	Index    int                `json:"output_index,omitempty"`
	Response *codexResponseInfo `json:"response,omitempty"` // for response.created
}

type codexResponseInfo struct {
	ID string `json:"id"`
	// We only need the ID; other fields (object, status, …) are ignored.
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

	case "response.function_call_arguments.delta":
		t.argBuffer.WriteString(ev.Delta)
		return nil

	case "response.function_call_arguments.done":
		var emissions []emission
		switch {
		case t.currentToolRenameTo != "":
			// Renamed tool (Bash, view_image): rename keys then sanitize.
			renamedJSON := reverseRenameArgs(t.currentToolRenameTo, t.argBuffer.String())
			renamedJSON = sanitizeJSONStringForTool(t.currentBlockClaudeName, renamedJSON)
			body, _ := json.Marshal(map[string]any{
				"type":  "content_block_delta",
				"index": t.currentBlockIdx,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": renamedJSON},
			})
			emissions = append(emissions, emission{name: "content_block_delta", data: string(body)})
		default:
			// Passthrough tool. Sanitize against the OUTBOUND name
			// (which equals the Claude name).
			passthrough := sanitizeJSONStringForTool(t.currentBlockClaudeName, t.argBuffer.String())
			if passthrough != "" {
				body, _ := json.Marshal(map[string]any{
					"type":  "content_block_delta",
					"index": t.currentBlockIdx,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": passthrough},
				})
				emissions = append(emissions, emission{name: "content_block_delta", data: string(body)})
			}
		}
		t.currentToolRenameTo = ""
		t.argBuffer.Reset()
		emissions = append(emissions, t.closeBlock()...)
		return emissions

	case "response.completed":
		var em []emission
		em = append(em, t.flushMessageDelta(ev)...)
		em = append(em, emission{name: "message_stop", data: `{"type":"message_stop"}`})
		t.messageEnded = true
		return em

	case "response.failed":
		errBody, _ := json.Marshal(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": "codex upstream returned response.failed",
			},
		})
		return []emission{{name: "error", data: string(errBody)}}

	// Dropped event types:
	// response.content_part.added   — parent output_item.added already opened the block
	// response.reasoning_summary_part.added — same reason
	// response.output_item.done     — block is closed by inner _text.done / _arguments.done
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
		callName := ev.Item.Name
		t.currentToolRenameTo = ""
		if claude, ok := lookupReverseName(ev.Item.Name); ok {
			callName = claude
			t.currentToolRenameTo = ev.Item.Name
		}
		t.currentBlockClaudeName = callName
		t.argBuffer.Reset()
		content = map[string]any{
			"type":  "tool_use",
			"id":    ev.Item.CallID,
			"name":  callName,
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

func (t *StreamTranslator) flushMessageDelta(ev codexEvent) []emission {
	// Use currentType if a block is still open; fall back to lastClosedType.
	// This handles the common case where the final block was already closed by
	// its _done event before response.completed arrives.
	blockType := t.currentType
	if blockType == "" {
		blockType = t.lastClosedType
	}
	stop := mapStopReason(ev.Status, blockType)
	usageOut := map[string]any{
		"input_tokens":  0,
		"output_tokens": 0,
	}
	if ev.Usage != nil {
		usageOut["input_tokens"] = ev.Usage.InputTokens
		usageOut["output_tokens"] = ev.Usage.OutputTokens
		cachedRead := 0
		if ev.Usage.InputTokensDetails != nil {
			cachedRead = ev.Usage.InputTokensDetails.CachedTokens
		}
		if cachedRead > 0 {
			usageOut["cache_read_input_tokens"] = cachedRead
		}
		// Store for FinalUsage getter.
		t.usage = &anthropicUsage{
			InputTokens:          ev.Usage.InputTokens,
			OutputTokens:         ev.Usage.OutputTokens,
			CacheReadInputTokens: cachedRead,
		}
	}
	body, _ := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": usageOut,
	})
	return []emission{{name: "message_delta", data: string(body)}}
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
