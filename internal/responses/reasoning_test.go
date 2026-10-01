package responses

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestReasoningSignature_RoundTrip(t *testing.T) {
	sum := json.RawMessage(`[{"type":"summary_text","text":"thought"}]`)
	sig := encodeReasoningSignature("rs_abc", sum, "ENC==", "grok-4.7")
	if !strings.HasPrefix(sig, "ccmrs1.") {
		t.Fatalf("sig %q lacks prefix", sig)
	}
	item, ok := decodeReasoningSignature(sig, "grok-4.7")
	if !ok {
		t.Fatal("decode failed")
	}
	if item.Type != "reasoning" || item.ID != "rs_abc" || item.EncryptedContent != "ENC==" || string(item.Summary) != string(sum) {
		t.Errorf("item = %+v", item)
	}
}

func TestReasoningSignature_EmptySummaryBecomesArray(t *testing.T) {
	item, ok := decodeReasoningSignature(encodeReasoningSignature("rs_1", nil, "E", ""), "")
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
		if _, ok := decodeReasoningSignature(sig, ""); ok {
			t.Errorf("%s: decoded, want rejection", name)
		}
	}
}

func TestReasoningSignature_MissingSummaryDecodesAsEmptyArray(t *testing.T) {
	sig := reasoningSigPrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"rs_1","encrypted_content":"E"}`))
	item, ok := decodeReasoningSignature(sig, "")
	if !ok || string(item.Summary) != "[]" {
		t.Errorf("item=%+v ok=%v", item, ok)
	}
}

func TestReasoningSignature_NullSummaryDecodesAsEmptyArray(t *testing.T) {
	sig := reasoningSigPrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"rs_1","summary":null,"encrypted_content":"E"}`))
	item, ok := decodeReasoningSignature(sig, "")
	if !ok || string(item.Summary) != "[]" {
		t.Errorf("summary = %s ok=%v, want []", item.Summary, ok)
	}
}

func TestReasoningSignature_SameModelRoundTrips(t *testing.T) {
	sig := encodeReasoningSignature("rs_1", nil, "E", "grok-4.7")
	if !strings.Contains(sigPayload(t, sig), `"m":"grok-4.7"`) {
		t.Errorf("payload %s lacks the producing model", sigPayload(t, sig))
	}
	if item, ok := decodeReasoningSignature(sig, "grok-4.7"); !ok || item.ID != "rs_1" {
		t.Errorf("same model: item=%+v ok=%v, want decoded", item, ok)
	}
}

func TestReasoningSignature_DifferentModelDropped(t *testing.T) {
	sig := encodeReasoningSignature("rs_1", nil, "E", "grok-4.7")
	if _, ok := decodeReasoningSignature(sig, "grok-4.6"); ok {
		t.Error("reasoning minted by grok-4.7 must not replay to grok-4.6")
	}
}

func TestReasoningSignature_MissingModelAccepted(t *testing.T) {
	sig := encodeReasoningSignature("rs_1", nil, "E", "")
	if strings.Contains(sigPayload(t, sig), `"m"`) {
		t.Errorf("empty model must be omitted from the payload: %s", sigPayload(t, sig))
	}
	if _, ok := decodeReasoningSignature(sig, "grok-4.7"); !ok {
		t.Error("signature without a model must stay accepted")
	}
}

func sigPayload(t *testing.T, sig string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sig, reasoningSigPrefix))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
