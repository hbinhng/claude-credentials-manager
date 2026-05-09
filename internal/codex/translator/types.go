// Package translator implements pure request and stream translation
// between the Anthropic Messages API and the OpenAI Responses API used
// by codex. No HTTP, no I/O — every function is testable in isolation.
//
// See spec §5 (request translation) and §6 (stream translation).
package translator

// Anthropic request shape — the inbound /v1/messages body.
type anthropicRequest struct {
	Model         string                 `json:"model"`
	Messages      []anthropicMessage     `json:"messages"`
	System        any                    `json:"system,omitempty"`        // string or []anthropicContentBlock
	Tools         []anthropicTool        `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoice   `json:"tool_choice,omitempty"`
	Thinking      *anthropicThinkingPref `json:"thinking,omitempty"`
	MaxTokens     int                    `json:"max_tokens,omitempty"`     // dropped on outbound
	Temperature   *float64               `json:"temperature,omitempty"`    // dropped
	TopP          *float64               `json:"top_p,omitempty"`          // dropped
	TopK          *int                   `json:"top_k,omitempty"`          // dropped
	StopSequences []string               `json:"stop_sequences,omitempty"` // dropped
	Stream        bool                   `json:"stream,omitempty"`         // forced true outbound
	Metadata      map[string]any         `json:"metadata,omitempty"`       // dropped
}

type anthropicMessage struct {
	Role    string                 `json:"role"` // "user" | "assistant"
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type   string             `json:"type"` // "text" | "image" | "tool_use" | "tool_result" | "thinking" | "redacted_thinking"
	Text   string             `json:"text,omitempty"`
	Source *anthropicImageSrc `json:"source,omitempty"`
	// tool_use:
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
	// tool_result:
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"` // string or []anthropicContentBlock
	IsError   bool   `json:"is_error,omitempty"`
	// thinking:
	Thinking string `json:"thinking,omitempty"`
}

type anthropicImageSrc struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/png" etc
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"` // "auto" | "any" | "tool"
	Name string `json:"name,omitempty"` // for type:"tool"
}

type anthropicThinkingPref struct {
	Type         string `json:"type"`                   // "enabled" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// Codex request shape — the outbound /backend-api/codex/responses body.
// Per spec 2026-05-09-codex-omniroute-pivot §5.2 we drop
// `prompt_cache_key` and `client_metadata` (OmniRoute production
// evidence shows chatgpt.com tolerates synthesis-only requests; these
// fields are not part of OmniRoute's outbound shape). Removing them
// from the type makes accidental re-introduction impossible.
type codexRequest struct {
	Model        string          `json:"model"`
	Stream       bool            `json:"stream"`
	Input        []codexInput    `json:"input"`
	Tools        []codexTool     `json:"tools,omitempty"`
	ToolChoice   any             `json:"tool_choice,omitempty"`
	Reasoning    *codexReasoning `json:"reasoning,omitempty"`
	Store        bool            `json:"store"`
	ServiceTier  string          `json:"service_tier,omitempty"`
	Instructions string          `json:"instructions,omitempty"` // we leave empty per spec §5.1
}

type codexInput struct {
	Type    string         `json:"type"` // "message" | "function_call" | "function_call_output"
	Role    string         `json:"role,omitempty"`
	Content []codexContent `json:"content,omitempty"`
	// function_call:
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output:
	Output string `json:"output,omitempty"`
}

type codexContent struct {
	Type     string `json:"type"` // "input_text" | "output_text" | "input_image"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type codexTool struct {
	Type        string         `json:"type"`                   // "function"
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type codexReasoning struct {
	Effort string `json:"effort"` // "low" | "medium" | "high" | "xhigh"
}
