package translator

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

	// Anti-repetition pressure. Codex CLI sends nothing → chatgpt.com
	// defaults both penalties to 0.0 → loops on non-Anthropic models.
	// Spec §Phase 1: hardcoded 0.4 (half the API range maximum).
	fp := 0.4
	pp := 0.4
	out.FrequencyPenalty = &fp
	out.PresencePenalty = &pp

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
		out.Tools = make([]codexTool, 0, len(in.Tools)+1)
		readPresent := false
		viewImagePresent := false
		for _, t := range in.Tools {
			if isDroppedClaudeTool(t.Name) {
				continue
			}
			out.Tools = append(out.Tools, applyForwardToolDef(t))
			if t.Name == "Read" {
				readPresent = true
			}
			if t.Name == "view_image" {
				viewImagePresent = true
			}
		}
		if readPresent && !viewImagePresent {
			r := toolRenameMap["Read"]
			out.Tools = append(out.Tools, codexTool{
				Type:        "function",
				Name:        r.To,
				Description: "Open a local image or PDF for visual inspection.",
				Parameters:  r.OutputSchema,
			})
		}
	}

	// tool_choice
	if in.ToolChoice != nil {
		out.ToolChoice = translateToolChoice(in.ToolChoice, out.Tools)
	}

	// thinking.budget_tokens → reasoning.effort
	if in.Thinking != nil && in.Thinking.Type == "enabled" {
		eff := bucketEffort(in.Thinking.BudgetTokens)
		if eff != "" {
			out.Reasoning = &codexReasoning{Effort: eff}
		}
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
			if role == "user" {
				text = stripDroppedReminders(text)
			}
			if text == "" || strings.TrimSpace(text) == "" {
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
			if isDroppedClaudeTool(b.Name) {
				continue // historical call to a dropped tool — skip
			}
			if len(msgContent) > 0 {
				out.Input = append(out.Input, codexInput{Type: "message", Role: role, Content: msgContent})
				msgContent = nil
			}
			callName := b.Name
			if r, ok := lookupForwardRename(b.Name); ok {
				callName = r.To
			}
			renamedInput := applyForwardArgRename(b.Name, b.Input)
			args, _ := json.Marshal(renamedInput)
			out.Input = append(out.Input, codexInput{
				Type:      "function_call",
				CallID:    stripStoredPrefix(b.ID),
				Name:      callName,
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
// string codex's function_call_output.output expects. Claude Code
// emits arrays mixing {type:"text"} and {type:"image"} blocks (e.g.
// from Bash and FileReadTool). Text is concatenated newline-separated;
// base64 images are passed through verbatim as data URIs so codex
// sees them rather than silently losing the image. Non-base64 image
// sources (url) are dropped, matching the message-content handling
// in appendMessageInput.
func stringifyToolResult(content any) string {
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
		// Apply forward rename so "Bash" resolves to "exec_command"
		// after Phase 4's rename. Without this, callers sending
		// tool_choice:{type:"tool",name:"Bash"} would silently lose the
		// forced-tool constraint because out.Tools no longer contains
		// "Bash".
		resolvedName := tc.Name
		if r, ok := lookupForwardRename(tc.Name); ok {
			resolvedName = r.To
		}
		for _, t := range tools {
			if t.Name == resolvedName {
				return map[string]any{"type": "function", "name": resolvedName}
			}
		}
		return nil
	}
	return nil
}

func bucketEffort(budget int) string {
	switch {
	case budget <= 0:
		return ""
	case budget <= 1024:
		return "low"
	case budget <= 10240:
		return "medium"
	case budget < 131072:
		return "high"
	default:
		return "xhigh"
	}
}
