package translator

import (
	"encoding/json"
	"errors"
	"fmt"
)

// RequestOpts configures TranslateRequest. Per spec
// 2026-05-09-codex-omniroute-pivot §5.2, the InstallationID and
// PromptCacheKey fields from sub-project B are dropped — neither
// field is emitted in the outbound body anymore.
type RequestOpts struct {
	TargetModel string // post-alias model name to send upstream
	ServiceTier string // verbatim into outbound; "" → field omitted
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

	// system → developer message at index 0 of input[]
	if sys := flattenSystem(in.System); sys != "" {
		out.Input = append(out.Input, codexInput{
			Type: "message",
			Role: "developer",
			Content: []codexContent{{Type: "input_text", Text: sys}},
		})
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
			cType := "input_text"
			if role == "assistant" {
				cType = "output_text"
			}
			msgContent = append(msgContent, codexContent{Type: cType, Text: b.Text})
		case "image":
			if b.Source != nil && b.Source.Type == "base64" {
				dataURL := "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				msgContent = append(msgContent, codexContent{Type: "input_image", ImageURL: dataURL})
			}
		case "tool_use":
			// Becomes its own function_call input item; emit message
			// content first if any has accumulated, then the function_call.
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

func stringifyToolResult(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		// Best-effort: serialize each item's text or just JSON-encode.
		out := ""
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if txt, _ := m["text"].(string); txt != "" {
				out += txt
			}
		}
		if out != "" {
			return out
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
		// Drop if name not in tools[] per spec §5.4.
		for _, t := range tools {
			if t.Name == tc.Name {
				return map[string]any{"type": "function", "name": tc.Name}
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
