package translator

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// RequestOpts configures TranslateRequest. Per spec
// 2026-05-09-codex-omniroute-bridging §5.4:
//   - SessionID is the inbound X-Claude-Code-Session-Id (UUIDv7).
//     Populates the outbound `prompt_cache_key` body field when
//     non-empty; omitted otherwise.
type RequestOpts struct {
	TargetModel string
	ServiceTier string
	SessionID   string
}

// Errors returned by TranslateRequest.
var (
	ErrInvalidJSON  = errors.New("translator: inbound body is not valid JSON")
	ErrMissingModel = errors.New("translator: inbound body has no model field")
)

// TranslateRequest converts an Anthropic /v1/messages body to an OpenAI
// /v1/responses body. Pure function; no HTTP, no I/O.
func TranslateRequest(claudeBody []byte, opts RequestOpts) ([]byte, error) {
	var in anthropicRequest
	if err := json.Unmarshal(claudeBody, &in); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	if in.Model == "" && opts.TargetModel == "" {
		return nil, ErrMissingModel
	}

	out := codexRequest{
		Model:       opts.TargetModel,
		Stream:      true, // forced per spec §5.1
		Store:       false,
		ServiceTier: opts.ServiceTier,
	}

	// Per spec 2026-05-09-codex-omniroute-bridging §5.4: hoist
	// inbound system content to the top-level `instructions` field
	// (NOT a developer-role item in input[]). Fall back to OmniRoute's
	// CODEX_CHAT_DEFAULT_INSTRUCTIONS when inbound has no system
	// content — chatgpt.com requires the field on every request.
	if sys := flattenSystem(in.System); sys != "" {
		out.Instructions = sys
	} else {
		out.Instructions = "You are a ChatGPT agent."
	}

	// Per spec §5.4: populate prompt_cache_key from the inbound session
	// ID. Empty SessionID → field omitted via the struct's omitempty.
	if opts.SessionID != "" {
		out.PromptCacheKey = opts.SessionID
	}

	// messages[] → input[]
	for _, m := range in.Messages {
		appended, err := appendMessageInput(&out, m)
		if err != nil {
			return nil, err
		}
		_ = appended
	}

	// Empty input[] guard: codex rejects zero-input requests; synthesize
	// a placeholder per spec §5.4.
	if len(out.Input) == 0 {
		out.Input = []codexInput{{
			Type: "message",
			Role: "user",
			Content: []codexContent{{Type: "input_text", Text: "continue"}},
		}}
	}

	// Drop orphan tool_results (no matching tool_use in same request).
	out.Input = dropOrphanToolResults(out.Input)

	// tools → flat function tools
	if len(in.Tools) > 0 {
		out.Tools = make([]codexTool, 0, len(in.Tools))
		for _, t := range in.Tools {
			out.Tools = append(out.Tools, codexTool{
				Type:        "function",
				Name:        t.Name,
				Description: defaultDescription(t.Description),
				Parameters:  defaultParameters(t.InputSchema),
			})
		}
	}

	// tool_choice
	if in.ToolChoice != nil {
		out.ToolChoice = translateToolChoice(in.ToolChoice, out.Tools)
	}

	// Always emit reasoning.effort — defaults to "none" when the
	// client expressed no thinking intent.
	out.Reasoning = &codexReasoning{Effort: resolveReasoningEffort(&in)}

	// Forward Anthropic max_tokens as chatgpt.com max_output_tokens so
	// the model gets the budget Claude Code requested. Without this,
	// chatgpt.com applies its own (often smaller) default and bails
	// with response.incomplete{reason:max_output_tokens} before any
	// visible output is produced — see followup notes on the post-
	// translator-stream-fix codex.txt capture.
	if in.MaxTokens > 0 {
		out.MaxOutputTokens = in.MaxTokens
	}

	return json.Marshal(out)
}

func flattenSystem(sys any) string {
	switch v := sys.(type) {
	case string:
		return v
	case []any:
		var s string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t == "text" {
				if txt, _ := m["text"].(string); txt != "" {
					if s != "" {
						s += "\n"
					}
					s += txt
				}
			}
		}
		return s
	}
	return ""
}

func appendMessageInput(out *codexRequest, m anthropicMessage) (bool, error) {
	role := m.Role
	if role != "user" && role != "assistant" {
		return false, fmt.Errorf("translator: unsupported role %q", role)
	}

	var msgContent []codexContent
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			text := b.Text
			if text == "" {
				continue
			}
			cType := "input_text"
			if role == "assistant" {
				cType = "output_text"
			}
			msgContent = append(msgContent, codexContent{Type: cType, Text: text})
		case "image":
			if b.Source != nil && b.Source.Type == "base64" {
				dataURL := "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				msgContent = append(msgContent, codexContent{Type: "input_image", ImageURL: dataURL})
			}
		case "tool_use":
			if len(msgContent) > 0 {
				out.Input = append(out.Input, codexInput{Type: "message", Role: role, Content: msgContent})
				msgContent = nil
			}
			args, _ := json.Marshal(b.Input)
			out.Input = append(out.Input, codexInput{
				Type:      "function_call",
				CallID:    stripStoredPrefix(b.ID),
				Name:      b.Name,
				Arguments: string(args),
			})
		case "tool_result":
			if len(msgContent) > 0 {
				out.Input = append(out.Input, codexInput{Type: "message", Role: role, Content: msgContent})
				msgContent = nil
			}
			out.Input = append(out.Input, codexInput{
				Type:   "function_call_output",
				CallID: stripStoredPrefix(b.ToolUseID),
				Output: stringifyToolResult(b.Content),
			})
		case "thinking", "redacted_thinking":
			// Dropped on request side per spec §5.1.
			continue
		}
	}
	if len(msgContent) > 0 {
		out.Input = append(out.Input, codexInput{Type: "message", Role: role, Content: msgContent})
	}
	return true, nil
}

// stripStoredPrefix removes server-prefixed IDs like rs_*, fc_*, resp_*,
// msg_* per spec §5.4.
func stripStoredPrefix(id string) string {
	for _, p := range []string{"rs_", "fc_", "resp_", "msg_"} {
		if len(id) > len(p) && id[:len(p)] == p {
			return id[len(p):]
		}
	}
	return id
}

// stringifyToolResult flattens tool_result content into the single
// string codex's function_call_output.output expects and caps it at
// toolResultMaxBytes on a whitespace boundary to avoid blowing
// chatgpt.com's per-turn input limit.
func stringifyToolResult(content any) string {
	return truncateAtWordBoundary(rawStringifyToolResult(content), toolResultMaxBytes)
}

// rawStringifyToolResult is the core flattening logic. Claude Code
// emits arrays mixing {type:"text"} and {type:"image"} blocks (e.g.
// from Bash and FileReadTool). Text is concatenated newline-separated;
// base64 images are passed through verbatim as data URIs so codex
// sees them rather than silently losing the image. Non-base64 image
// sources (url) are dropped, matching the message-content handling
// in appendMessageInput.
func rawStringifyToolResult(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch t, _ := m["type"].(string); t {
			case "text":
				if txt, _ := m["text"].(string); txt != "" {
					parts = append(parts, txt)
				}
			case "image":
				src, ok := m["source"].(map[string]any)
				if !ok {
					continue
				}
				if st, _ := src["type"].(string); st != "base64" {
					continue
				}
				mt, _ := src["media_type"].(string)
				data, _ := src["data"].(string)
				if data == "" {
					continue
				}
				parts = append(parts, "data:"+mt+";base64,"+data)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	if content != nil {
		b, _ := json.Marshal(content)
		return string(b)
	}
	return ""
}

func dropOrphanToolResults(input []codexInput) []codexInput {
	// Build a set of call_ids from function_call items.
	callIDs := map[string]bool{}
	for _, it := range input {
		if it.Type == "function_call" && it.CallID != "" {
			callIDs[it.CallID] = true
		}
	}
	out := input[:0]
	for _, it := range input {
		if it.Type == "function_call_output" && !callIDs[it.CallID] {
			continue // drop
		}
		out = append(out, it)
	}
	return out
}

func defaultDescription(d string) string {
	if d == "" {
		return " " // spec §5.4
	}
	return d
}

func defaultParameters(s map[string]any) map[string]any {
	if len(s) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return s
}

func translateToolChoice(tc *anthropicToolChoice, tools []codexTool) any {
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		for _, t := range tools {
			if t.Name == tc.Name {
				return map[string]any{"type": "function", "name": tc.Name}
			}
		}
		return nil
	}
	return nil
}

// resolveReasoningEffort returns the codex reasoning effort label
// for the request. Always returns a non-empty value — defaults to
// "none" when the client expressed no thinking intent.
//
// Priority:
//  1. output_config.effort label (1:1 map; max/xhigh → xhigh).
//     Unknown labels fall through to budget bucketing.
//  2. thinking.type == "enabled" with budget_tokens → bucket.
//  3. Default: "none".
func resolveReasoningEffort(req *anthropicRequest) string {
	if req.OutputConfig != nil {
		switch req.OutputConfig.Effort {
		case "low", "medium", "high":
			return req.OutputConfig.Effort
		case "max", "xhigh":
			return "xhigh"
		}
	}
	if req.Thinking != nil && req.Thinking.Type == "enabled" {
		switch b := req.Thinking.BudgetTokens; {
		case b <= 0:
			return "none"
		case b <= 256:
			return "minimal"
		case b <= 1024:
			return "low"
		case b <= 10240:
			return "medium"
		case b <= 131071:
			return "high"
		default:
			return "xhigh"
		}
	}
	return "none"
}

// toolResultMaxBytes caps the size of a forwarded tool_result string.
// Set well below the model's per-turn token budget; the truncator
// breaks at the last whitespace at or before the cap so a partial
// word like the observed "Updated task #1 ___" is avoided.
const toolResultMaxBytes = 64 * 1024

// truncateAtWordBoundary returns s if len(s) <= max, otherwise the
// longest prefix of length <= max that ends at a whitespace boundary.
// If no whitespace exists in the prefix, falls back to a hard cut at
// max. Callers (currently stringifyToolResult) use this to keep
// over-long tool_result strings from blowing chatgpt.com's input
// limit while preserving readability.
func truncateAtWordBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := lastWhitespaceIndex(cut); i >= 0 {
		return cut[:i]
	}
	// No whitespace — back up byte-by-byte to a UTF-8 rune boundary
	// so we don't split a multi-byte codepoint and produce invalid
	// UTF-8.
	for len(cut) > 0 && !utf8.RuneStart(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	// If the last byte is a multi-byte rune start (0b11xxxxxx) but
	// its continuation bytes were stripped, the rune is incomplete.
	// Drop the start byte too.
	if len(cut) > 0 {
		r, n := utf8.DecodeLastRuneInString(cut)
		if r == utf8.RuneError && n == 1 {
			cut = cut[:len(cut)-1]
		}
	}
	return cut
}

func lastWhitespaceIndex(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ' ', '\t', '\n':
			return i
		}
	}
	return -1
}
