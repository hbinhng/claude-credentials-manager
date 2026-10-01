package responses

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestReasoningSignature_RoundTrip(t *testing.T) {
	sum := json.RawMessage(`[{"type":"summary_text","text":"thought"}]`)
	sig := encodeReasoningSignature("rs_abc", sum, "ENC==")
	if !strings.HasPrefix(sig, "ccmrs1.") {
		t.Fatalf("sig %q lacks prefix", sig)
	}
	item, ok := decodeReasoningSignature(sig)
	if !ok {
		t.Fatal("decode failed")
	}
	if item.Type != "reasoning" || item.ID != "rs_abc" || item.EncryptedContent != "ENC==" || string(item.Summary) != string(sum) {
		t.Errorf("item = %+v", item)
	}
}

func TestReasoningSignature_EmptySummaryBecomesArray(t *testing.T) {
	item, ok := decodeReasoningSignature(encodeReasoningSignature("rs_1", nil, "E"))
	if !ok || string(item.Summary) != "[]" {
		t.Errorf("summary = %s ok=%v, want []", item.Summary, ok)
	}
}

func TestReasoningSignature_RejectsForeignAndCorrupt(t *testing.T) {
	b64 := func(s string) string { return reasoningSigPrefix + base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, sig := range map[string]string{
		"anthropic signature": "EqQBCkgIARABGAIiQK0",
		"empty":               "",
		"bad base64":          reasoningSigPrefix + "!!!",
		"not json":            b64("nope"),
		"missing id":          b64(`{"summary":[],"encrypted_content":"E"}`),
		"missing encrypted":   b64(`{"id":"rs_1","summary":[]}`),
	} {
		if _, ok := decodeReasoningSignature(sig); ok {
			t.Errorf("%s: decoded, want rejection", name)
		}
	}
}

func TestReasoningSignature_MissingSummaryDecodesAsEmptyArray(t *testing.T) {
	sig := reasoningSigPrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"rs_1","encrypted_content":"E"}`))
	item, ok := decodeReasoningSignature(sig)
	if !ok || string(item.Summary) != "[]" {
		t.Errorf("item=%+v ok=%v", item, ok)
	}
}
