package responses

import "strings"

// Dialect captures every way an upstream's Responses API flavour differs.
// The zero value is codex (chatgpt.com) behaviour, so RequestOpts{} and
// StreamOpts{} keep translating exactly as before Dialect existed.
type Dialect struct {
	// SystemAsInputItem sends the system prompt as input[0]
	// {role:"system"} instead of the top-level instructions field, and
	// skips codex's "You are a ChatGPT agent." fallback.
	SystemAsInputItem bool
	// MidSystemRole is the role used for Claude Code's mid-conversation
	// role:"system" messages. "" means "developer".
	MidSystemRole string
	// StringContent sends message content as a plain string (grok-shell's
	// shape) instead of a parts array. Content carrying an image keeps the
	// parts array.
	StringContent bool
	// ReasoningSummary is reasoning.summary when effort != none. "" means
	// "auto".
	ReasoningSummary string
	// Include, when non-nil, is sent on every request. nil keeps codex's
	// rule: ["reasoning.encrypted_content"] only when effort != none.
	Include []string
	// OmitNoneEffort drops the whole reasoning object when effort resolves
	// to "none" (grok rejects effort "none" with HTTP 400).
	OmitNoneEffort bool
	// CarryReasoning round-trips reasoning items through thinking-block
	// signatures: the stream emits one signed thinking block per reasoning
	// item, and the request side turns signed thinking blocks back into
	// reasoning input items.
	CarryReasoning bool
}

// Codex is chatgpt.com's dialect (the zero value).
var Codex = Dialect{}

// Grok is cli-chat-proxy.grok.com's dialect, mirroring grok-shell 1.0.46
// (captured 2026-10-01).
var Grok = Dialect{
	SystemAsInputItem: true,
	MidSystemRole:     "system",
	StringContent:     true,
	ReasoningSummary:  "concise",
	Include:           []string{"reasoning.encrypted_content", "no_inline_citations"},
	OmitNoneEffort:    true,
	CarryReasoning:    true,
}

func (d Dialect) midSystemRole() string {
	if d.MidSystemRole == "" {
		return "developer"
	}
	return d.MidSystemRole
}

func (d Dialect) reasoningSummary() string {
	if d.ReasoningSummary == "" {
		return "auto"
	}
	return d.ReasoningSummary
}

// message builds a message input item. Under StringContent, text-only
// content collapses to one newline-joined string; content carrying an image
// keeps the parts array.
func (d Dialect) message(role string, parts []codexContent) codexInput {
	if !d.StringContent {
		return codexInput{Type: "message", Role: role, Content: parts}
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Type == "input_image" {
			return codexInput{Type: "message", Role: role, Content: parts}
		}
		texts = append(texts, p.Text)
	}
	return codexInput{Type: "message", Role: role, Content: strings.Join(texts, "\n")}
}
