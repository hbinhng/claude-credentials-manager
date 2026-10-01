package responses_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/responses"
)

func loadCC2286(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/cc-2.1.286-opus55.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type ccItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func translateCC2286(t *testing.T, d responses.Dialect) (map[string]json.RawMessage, []ccItem) {
	t.Helper()
	out, err := responses.TranslateRequest(loadCC2286(t), responses.RequestOpts{TargetModel: "m", SessionID: "s", Dialect: d})
	if err != nil {
		t.Fatalf("Claude Code 2.1.286 body must translate, got: %v", err)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(out, &top)
	var items []ccItem
	_ = json.Unmarshal(top["input"], &items)
	return top, items
}

func TestCC2286_Codex_MidSystemBecomesDeveloperInPlace(t *testing.T) {
	top, items := translateCC2286(t, responses.Codex)
	if len(items) != 2 || items[0].Role != "user" || items[1].Role != "developer" {
		t.Fatalf("items = %+v, want [user, developer]", items)
	}
	var parts []map[string]any
	_ = json.Unmarshal(items[1].Content, &parts)
	if len(parts) != 1 || parts[0]["type"] != "input_text" || parts[0]["text"] != "SessionStart hook additional context: HOOK" {
		t.Errorf("developer content = %s", items[1].Content)
	}
	var instr string
	_ = json.Unmarshal(top["instructions"], &instr)
	if instr == "" {
		t.Error("codex instructions must carry the top-level system prompt")
	}
	var r map[string]string
	_ = json.Unmarshal(top["reasoning"], &r)
	if r["effort"] != "medium" {
		t.Errorf("effort = %q, want medium", r["effort"])
	}
}

func TestCC2286_Grok_MidSystemStaysInPlaceAsSystem(t *testing.T) {
	_, items := translateCC2286(t, responses.Grok)
	if len(items) != 3 || items[0].Role != "system" || items[1].Role != "user" || items[2].Role != "system" {
		t.Fatalf("items = %+v, want [system(prompt), user, system(hook)]", items)
	}
	var s string
	_ = json.Unmarshal(items[2].Content, &s)
	if s != "SessionStart hook additional context: HOOK" {
		t.Errorf("mid-conversation system content = %q", s)
	}
}
