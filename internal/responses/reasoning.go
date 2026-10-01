package responses

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// reasoningSigPrefix marks a thinking-block signature that ccm minted to
// carry an upstream reasoning item through Claude Code's history. Anything
// else (a real Anthropic signature from before a model switch, a corrupt
// value) is not ours and is dropped.
const reasoningSigPrefix = "ccmrs1."

type reasoningSig struct {
	ID               string          `json:"id"`
	Summary          json.RawMessage `json:"summary"`
	EncryptedContent string          `json:"encrypted_content"`
	// Model is the upstream model that produced the reasoning. Encrypted
	// reasoning is only valid for that model, so it is not replayed after
	// a model switch. Omitted when unknown.
	Model string `json:"m,omitempty"`
}

// encodeReasoningSignature packs a reasoning output item into a thinking
// signature: "ccmrs1." + base64url(JSON{id, summary, encrypted_content, m}).
// The summary rides along because under thinking.display:"omitted" it is
// not in the thinking text, and grok-shell replays it. model is the
// upstream model that produced the item ("" omits it).
func encodeReasoningSignature(id string, summary json.RawMessage, encrypted, model string) string {
	if len(summary) == 0 {
		summary = json.RawMessage("[]")
	}
	b, _ := json.Marshal(reasoningSig{ID: id, Summary: summary, EncryptedContent: encrypted, Model: model})
	return reasoningSigPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// decodeReasoningSignature reverses encodeReasoningSignature into a
// reasoning input item. ok is false for any signature ccm did not mint, and
// for one minted by a model other than targetModel (a signature without a
// model is accepted).
func decodeReasoningSignature(sig, targetModel string) (codexInput, bool) {
	rest, ok := strings.CutPrefix(sig, reasoningSigPrefix)
	if !ok {
		return codexInput{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return codexInput{}, false
	}
	var s reasoningSig
	if err := json.Unmarshal(raw, &s); err != nil || s.ID == "" || s.EncryptedContent == "" {
		return codexInput{}, false
	}
	if s.Model != "" && s.Model != targetModel {
		return codexInput{}, false
	}
	if len(s.Summary) == 0 || string(s.Summary) == "null" {
		s.Summary = json.RawMessage("[]")
	}
	return codexInput{Type: "reasoning", ID: s.ID, Summary: s.Summary, EncryptedContent: s.EncryptedContent}, true
}

// EncodeReasoningSignatureForTest exposes encodeReasoningSignature to
// external test packages (terminal tests build signed thinking blocks).
func EncodeReasoningSignatureForTest(id string, summary json.RawMessage, encrypted string) string {
	return encodeReasoningSignature(id, summary, encrypted, "")
}

// ThinkingOmitted reports whether an inbound Anthropic request asked for
// thinking.display:"omitted" (Claude Code 2.1.286+ on Opus/Sonnet 5.5).
// Terminals pass it to StreamOpts.OmitThinkingText.
func ThinkingOmitted(body []byte) bool {
	var p struct {
		Thinking *struct {
			Display string `json:"display"`
		} `json:"thinking"`
	}
	if json.Unmarshal(body, &p) != nil || p.Thinking == nil {
		return false
	}
	return p.Thinking.Display == "omitted"
}
