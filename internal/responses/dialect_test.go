package responses_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/responses"
)

// translateMap runs TranslateRequest and decodes the result for assertions.
func translateMap(t *testing.T, body string, d responses.Dialect) map[string]any {
	t.Helper()
	out, err := responses.TranslateRequest([]byte(body), responses.RequestOpts{
		TargetModel: "grok-4.7", SessionID: "sess-1", Dialect: d,
	})
	if err != nil {
		t.Fatalf("TranslateRequest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestCodexDialectIsZeroValue(t *testing.T) {
	if !reflect.DeepEqual(responses.Codex, responses.Dialect{}) {
		t.Fatalf("responses.Codex must be the zero Dialect so RequestOpts{} keeps codex behaviour")
	}
}

func TestGrok_SystemIsFirstInputItemAsString(t *testing.T) {
	m := translateMap(t, `{"model":"x","system":[{"type":"text","text":"be terse"},{"type":"text","text":"cite nothing"}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":"there"}]}]}`, responses.Grok)
	if _, ok := m["instructions"]; ok {
		t.Errorf("grok must not send instructions, got %v", m["instructions"])
	}
	in := m["input"].([]any)
	first := in[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be terse\ncite nothing" {
		t.Errorf("input[0] = %v, want system item with flattened string content", first)
	}
	user := in[1].(map[string]any)
	if user["role"] != "user" || user["content"] != "hi\nthere" {
		t.Errorf("input[1] = %v, want user item with string content", user)
	}
}

func TestGrok_NoSystemMeansNoSystemItemAndNoInstructions(t *testing.T) {
	m := translateMap(t, `{"model":"x","messages":[{"role":"user","content":"hi"}]}`, responses.Grok)
	if _, ok := m["instructions"]; ok {
		t.Errorf("instructions present: %v", m["instructions"])
	}
	in := m["input"].([]any)
	if len(in) != 1 || in[0].(map[string]any)["role"] != "user" {
		t.Errorf("input = %v, want just the user item", in)
	}
}

func TestGrok_EmptyMessagesPlaceholderComesAfterSystem(t *testing.T) {
	m := translateMap(t, `{"model":"x","system":"sys","messages":[]}`, responses.Grok)
	in := m["input"].([]any)
	if len(in) != 2 || in[0].(map[string]any)["role"] != "system" || in[1].(map[string]any)["content"] != "continue" {
		t.Errorf("input = %v, want [system, placeholder user 'continue']", in)
	}
}

func TestGrok_ImageKeepsPartsArray(t *testing.T) {
	m := translateMap(t, `{"model":"x","messages":[{"role":"user","content":[
		{"type":"text","text":"look"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`, responses.Grok)
	content, ok := m["input"].([]any)[0].(map[string]any)["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("image message must keep parts array, got %v", m["input"])
	}
}

func TestGrok_ReasoningSummaryIncludeAndEffort(t *testing.T) {
	m := translateMap(t, `{"model":"x","output_config":{"effort":"xhigh"},"messages":[{"role":"user","content":"hi"}]}`, responses.Grok)
	r := m["reasoning"].(map[string]any)
	if r["effort"] != "xhigh" || r["summary"] != "concise" {
		t.Errorf("reasoning = %v, want effort xhigh (grok accepts it) + summary concise", r)
	}
	inc := m["include"].([]any)
	if len(inc) != 2 || inc[0] != "reasoning.encrypted_content" || inc[1] != "no_inline_citations" {
		t.Errorf("include = %v", inc)
	}
	if m["prompt_cache_key"] != "sess-1" || m["store"] != false || m["stream"] != true {
		t.Errorf("prompt_cache_key/store/stream wrong: %v %v %v", m["prompt_cache_key"], m["store"], m["stream"])
	}
}

func TestGrok_NoneEffortOmitsReasoningButKeepsInclude(t *testing.T) {
	m := translateMap(t, `{"model":"x","messages":[{"role":"user","content":"hi"}]}`, responses.Grok)
	if _, ok := m["reasoning"]; ok {
		t.Errorf("effort none must omit reasoning for grok (grok 400s on effort none), got %v", m["reasoning"])
	}
	if inc, _ := m["include"].([]any); len(inc) != 2 {
		t.Errorf("include must still be sent, got %v", m["include"])
	}
}

func TestGrok_AdaptiveThinkingUsesOutputConfigEffort(t *testing.T) {
	m := translateMap(t, `{"model":"x","thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"medium"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},
		"messages":[{"role":"user","content":"hi"}]}`, responses.Grok)
	if r := m["reasoning"].(map[string]any); r["effort"] != "medium" {
		t.Errorf("effort = %v, want medium from output_config", r["effort"])
	}
	if _, ok := m["context_management"]; ok {
		t.Error("context_management must not be forwarded")
	}
}

func TestCodex_AdaptiveThinkingUsesOutputConfigEffort(t *testing.T) {
	m := translateMap(t, `{"model":"x","thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"high"},
		"messages":[{"role":"user","content":"hi"}]}`, responses.Codex)
	r := m["reasoning"].(map[string]any)
	if r["effort"] != "high" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v, want high/auto", r)
	}
	if m["instructions"] != "You are a ChatGPT agent." {
		t.Errorf("codex instructions fallback lost: %v", m["instructions"])
	}
}

func TestGrok_AssistantTextIsString(t *testing.T) {
	m := translateMap(t, `{"model":"x","messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"text","text":"a"}]},{"role":"user","content":"q2"}]}`, responses.Grok)
	a := m["input"].([]any)[1].(map[string]any)
	if a["role"] != "assistant" || a["content"] != "a" {
		t.Errorf("assistant item = %v, want string content", a)
	}
}

func TestGrok_SignedThinkingBecomesReasoningItemInPlace(t *testing.T) {
	sig := responses.EncodeReasoningSignatureForTest("rs_1", json.RawMessage(`[{"type":"summary_text","text":"s"}]`), "ENC")
	body := `{"model":"x","messages":[
		{"role":"user","content":"run it"},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"","signature":"` + sig + `"},
			{"type":"text","text":"running"},
			{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"ls"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"a.txt"}]}]}`
	in := translateMap(t, body, responses.Grok)["input"].([]any)
	types := []string{}
	for _, it := range in {
		types = append(types, it.(map[string]any)["type"].(string))
	}
	want := []string{"message", "reasoning", "message", "function_call", "function_call_output"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	r := in[1].(map[string]any)
	if r["id"] != "rs_1" || r["encrypted_content"] != "ENC" {
		t.Errorf("reasoning item = %v", r)
	}
}

func TestGrok_ThinkingAfterTextFlushesPendingMessage(t *testing.T) {
	sig := responses.EncodeReasoningSignatureForTest("rs_2", nil, "ENC")
	body := `{"model":"x","messages":[{"role":"user","content":"q"},{"role":"assistant","content":[
		{"type":"text","text":"before"},
		{"type":"thinking","thinking":"","signature":"` + sig + `"},
		{"type":"text","text":"after"}]}]}`
	in := translateMap(t, body, responses.Grok)["input"].([]any)
	types := []string{}
	for _, it := range in {
		types = append(types, it.(map[string]any)["type"].(string))
	}
	if want := []string{"message", "message", "reasoning", "message"}; !reflect.DeepEqual(types, want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
}

func TestGrok_ForeignOrRedactedThinkingDropped(t *testing.T) {
	body := `{"model":"x","messages":[{"role":"user","content":"q"},{"role":"assistant","content":[
		{"type":"thinking","thinking":"t","signature":"EqQBCkgIARAB"},
		{"type":"redacted_thinking","data":"xx"},
		{"type":"text","text":"a"}]}]}`
	in := translateMap(t, body, responses.Grok)["input"].([]any)
	if len(in) != 2 {
		t.Fatalf("input = %v, want [user, assistant] only", in)
	}
}

func TestCodex_SignedThinkingStillDropped(t *testing.T) {
	sig := responses.EncodeReasoningSignatureForTest("rs_1", nil, "ENC")
	body := `{"model":"x","messages":[{"role":"user","content":"q"},{"role":"assistant","content":[
		{"type":"thinking","thinking":"","signature":"` + sig + `"},{"type":"text","text":"a"}]}]}`
	in := translateMap(t, body, responses.Codex)["input"].([]any)
	for _, it := range in {
		if it.(map[string]any)["type"] == "reasoning" {
			t.Fatal("codex (CarryReasoning off) must drop thinking")
		}
	}
}
