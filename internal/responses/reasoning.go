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
}

// encodeReasoningSignature packs a reasoning output item into a thinking
// signature: "ccmrs1." + base64url(JSON{id, summary, encrypted_content}).
// The summary rides along because under thinking.display:"omitted" it is
// not in the thinking text, and grok-shell replays it.
func encodeReasoningSignature(id string, summary json.RawMessage, encrypted string) string {
	if len(summary) == 0 {
		summary = json.RawMessage("[]")
	}
	b, _ := json.Marshal(reasoningSig{ID: id, Summary: summary, EncryptedContent: encrypted})
	return reasoningSigPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// decodeReasoningSignature reverses encodeReasoningSignature into a
// reasoning input item. ok is false for any signature ccm did not mint.
func decodeReasoningSignature(sig string) (codexInput, bool) {
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
	if len(s.Summary) == 0 {
		s.Summary = json.RawMessage("[]")
	}
	return codexInput{Type: "reasoning", ID: s.ID, Summary: s.Summary, EncryptedContent: s.EncryptedContent}, true
}

// EncodeReasoningSignatureForTest exposes encodeReasoningSignature to
// external test packages (terminal tests build signed thinking blocks).
func EncodeReasoningSignatureForTest(id string, summary json.RawMessage, encrypted string) string {
	return encodeReasoningSignature(id, summary, encrypted)
}
