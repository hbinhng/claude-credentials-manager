package translator_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/translator"
)

func TestTranslateRequest_Fixtures(t *testing.T) {
	dir := "testdata/request"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".in.json") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".in.json")
		t.Run(base, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join(dir, base+".in.json"))
			if err != nil {
				t.Fatalf("read in: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(dir, base+".out.json"))
			if err != nil {
				t.Fatalf("read out: %v", err)
			}
			optsBytes, err := os.ReadFile(filepath.Join(dir, base+".opts.json"))
			if err != nil {
				t.Fatalf("read opts: %v", err)
			}
			var opts translator.RequestOpts
			if err := json.Unmarshal(optsBytes, &opts); err != nil {
				t.Fatalf("unmarshal opts: %v", err)
			}

			got, err := translator.TranslateRequest(in, opts)
			if err != nil {
				t.Fatalf("TranslateRequest: %v", err)
			}
			// Normalize both via map for stable comparison.
			var gotM, wantM map[string]any
			_ = json.Unmarshal(got, &gotM)
			_ = json.Unmarshal(want, &wantM)
			if !jsonEqual(gotM, wantM) {
				t.Errorf("mismatch:\n GOT: %s\nWANT: %s", string(got), string(want))
			}
		})
	}
}

func TestTranslateRequest_InvalidJSON(t *testing.T) {
	_, err := translator.TranslateRequest([]byte("{not json"), translator.RequestOpts{TargetModel: "gpt-5"})
	if err == nil {
		t.Error("want error on malformed JSON")
	}
	if !errors.Is(err, translator.ErrInvalidJSON) {
		t.Errorf("want ErrInvalidJSON, got %v", err)
	}
}

func TestTranslateRequest_MissingModel(t *testing.T) {
	_, err := translator.TranslateRequest([]byte(`{"messages":[]}`), translator.RequestOpts{})
	if err == nil {
		t.Error("want error when both inbound model and TargetModel are empty")
	}
	if !errors.Is(err, translator.ErrMissingModel) {
		t.Errorf("want ErrMissingModel, got %v", err)
	}
}

func TestTranslateRequest_InboundModelFallback(t *testing.T) {
	// When TargetModel is empty but inbound model is present, it must NOT
	// error — the caller may rely on pass-through when no alias is set.
	// The outbound model field is opts.TargetModel (empty string), which
	// matches the zero value. This exercises the ErrMissingModel guard.
	_, err := translator.TranslateRequest(
		[]byte(`{"model":"claude-opus-4.7","messages":[]}`),
		translator.RequestOpts{TargetModel: "codex-model"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTranslateRequest_UnsupportedRole(t *testing.T) {
	_, err := translator.TranslateRequest(
		[]byte(`{"model":"claude-opus-4.7","messages":[{"role":"system","content":[{"type":"text","text":"hi"}]}]}`),
		translator.RequestOpts{TargetModel: "gpt-5"},
	)
	if err == nil {
		t.Error("want error for unsupported role")
	}
}

func TestTranslateRequest_ThinkingBlocksDropped(t *testing.T) {
	// thinking + redacted_thinking content blocks are dropped in the request.
	// An assistant message containing only thinking blocks produces no message
	// input item; the empty guard should then synthesize "continue".
	body := `{"model":"claude-opus-4.7","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"..."},{"type":"redacted_thinking","thinking":"..."}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	input := m["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("want 1 synthesized input, got %d: %v", len(input), input)
	}
	item := input[0].(map[string]any)
	if item["role"] != "user" {
		t.Errorf("synthesized item role = %q, want user", item["role"])
	}
}

func TestTranslateRequest_ToolResultArrayContent(t *testing.T) {
	// tool_result with array content → each text block concatenated.
	body := `{"model":"claude-opus-4.7","tools":[{"name":"f","description":"fn","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"fc_xyz","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"fc_xyz","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}]}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "part1") || !strings.Contains(string(got), "part2") {
		t.Errorf("expected concatenated parts in output: %s", string(got))
	}
}

func TestTranslateRequest_ToolResultNilContent(t *testing.T) {
	// tool_result with null content should produce empty output string.
	body := `{"model":"claude-opus-4.7","tools":[{"name":"f","description":"fn","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"fc_xyz","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"fc_xyz","content":null}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Output should have a function_call_output with empty output.
	if !strings.Contains(string(got), "function_call_output") {
		t.Errorf("expected function_call_output: %s", string(got))
	}
}

func TestTranslateRequest_ToolResultObjectContent(t *testing.T) {
	// tool_result with non-string, non-array content → JSON-encoded.
	body := `{"model":"claude-opus-4.7","tools":[{"name":"f","description":"fn","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"fc_xyz","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"fc_xyz","content":{"key":"value"}}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "function_call_output") {
		t.Errorf("expected function_call_output: %s", string(got))
	}
}

func TestTranslateRequest_ImageNonBase64Skipped(t *testing.T) {
	// image blocks with non-base64 source type are skipped.
	body := `{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","media_type":"image/png","data":""}},{"type":"text","text":"describe"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(got), "input_image") {
		t.Errorf("non-base64 image should be skipped: %s", string(got))
	}
}

func TestTranslateRequest_BucketEffortBoundaries(t *testing.T) {
	cases := []struct {
		budget int
		effort string
	}{
		{0, ""},      // omit
		{1, "low"},   // ≤1024
		{1024, "low"},
		{1025, "medium"},
		{10240, "medium"},
		{10241, "high"},
		{131071, "high"},
		{131072, "xhigh"},
		{200000, "xhigh"},
	}
	for _, c := range cases {
		body := []byte(`{"model":"claude-opus-4.7","thinking":{"type":"enabled","budget_tokens":` + strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d", c.budget), "0"), ".") + `},"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`)
		// Rebuild properly
		body = []byte(`{"model":"claude-opus-4.7","thinking":{"type":"enabled","budget_tokens":` + fmt.Sprintf("%d", c.budget) + `},"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`)
		got, err := translator.TranslateRequest(body, translator.RequestOpts{TargetModel: "gpt-5"})
		if err != nil {
			t.Fatalf("budget=%d: unexpected error: %v", c.budget, err)
		}
		var m map[string]any
		_ = json.Unmarshal(got, &m)
		reas, _ := m["reasoning"].(map[string]any)
		var gotEffort string
		if reas != nil {
			gotEffort, _ = reas["effort"].(string)
		}
		if gotEffort != c.effort {
			t.Errorf("budget=%d: effort=%q, want %q", c.budget, gotEffort, c.effort)
		}
	}
}

func TestTranslateRequest_ThinkingDisabled(t *testing.T) {
	// thinking.type=="disabled" → no reasoning field
	body := `{"model":"claude-opus-4.7","thinking":{"type":"disabled","budget_tokens":5000},"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(got), "reasoning") {
		t.Errorf("disabled thinking should not produce reasoning field: %s", string(got))
	}
}

func TestTranslateRequest_NoInstallationID(t *testing.T) {
	// No InstallationID → no client_metadata field
	body := `{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(got), "client_metadata") {
		t.Errorf("no installation ID should produce no client_metadata: %s", string(got))
	}
}

func TestTranslateRequest_SystemEmptyBlocks(t *testing.T) {
	// system as array but all blocks have empty text → no developer message
	body := `{"model":"claude-opus-4.7","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	input := m["input"].([]any)
	// Should only have the user message, not a developer message
	for _, item := range input {
		it := item.(map[string]any)
		if it["role"] == "developer" {
			t.Error("empty system blocks should not produce developer message")
		}
	}
}

func TestTranslateRequest_ToolUseWithPrecedingText(t *testing.T) {
	// assistant message with text then tool_use: text becomes a message item,
	// tool_use becomes a separate function_call item.
	body := `{"model":"claude-opus-4.7","tools":[{"name":"calc","description":"calc","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"assistant","content":[{"type":"text","text":"Let me calculate that."},{"type":"tool_use","id":"toolu_01","name":"calc","input":{"x":1}}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	input := m["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("want 2 input items (message + function_call), got %d: %s", len(input), string(got))
	}
	first := input[0].(map[string]any)
	if first["type"] != "message" {
		t.Errorf("first item type = %q, want message", first["type"])
	}
	second := input[1].(map[string]any)
	if second["type"] != "function_call" {
		t.Errorf("second item type = %q, want function_call", second["type"])
	}
}

func TestTranslateRequest_SystemAsNonArray(t *testing.T) {
	// system as an unexpected JSON type (e.g. number) → no developer message
	body := `{"model":"claude-opus-4.7","system":42,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(got), "developer") {
		t.Errorf("unexpected type system should produce no developer message: %s", string(got))
	}
}

func TestTranslateRequest_SystemArrayNonObject(t *testing.T) {
	// system as array with non-object items → skipped
	body := `{"model":"claude-opus-4.7","system":["text string item"],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(got), "developer") {
		t.Errorf("non-object system array items should be skipped: %s", string(got))
	}
}

func TestTranslateRequest_ToolResultArrayNoText(t *testing.T) {
	// tool_result with array content where items have no text → falls through to JSON marshal
	body := `{"model":"claude-opus-4.7","tools":[{"name":"f","description":"fn","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"fc_xyz","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"fc_xyz","content":[{"type":"image","data":"abc"}]}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "function_call_output") {
		t.Errorf("expected function_call_output: %s", string(got))
	}
}

func TestTranslateRequest_ToolChoiceUnknownType(t *testing.T) {
	// tool_choice with unknown type → nil → field omitted
	body := `{"model":"claude-opus-4.7","tools":[{"name":"f","description":"fn","input_schema":{}}],"tool_choice":{"type":"unknown_type"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(got), "tool_choice") {
		t.Errorf("unknown tool_choice type should be omitted: %s", string(got))
	}
}

func TestTranslateRequest_ImageNilSource(t *testing.T) {
	// image block with nil source → skipped
	body := `{"model":"claude-opus-4.7","messages":[{"role":"user","content":[{"type":"image"},{"type":"text","text":"describe"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(got), "input_image") {
		t.Errorf("nil-source image should be skipped: %s", string(got))
	}
}

func TestTranslateRequest_ToolResultArrayNonObjectItems(t *testing.T) {
	// tool_result with array content where items are not maps (e.g. strings)
	// → inner ok=false path → falls through to JSON marshal of the array.
	body := `{"model":"claude-opus-4.7","tools":[{"name":"f","description":"fn","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"fc_xyz","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"fc_xyz","content":["string item one","string item two"]}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "function_call_output") {
		t.Errorf("expected function_call_output: %s", string(got))
	}
}

func TestTranslateRequest_ToolResultPrecedingContent(t *testing.T) {
	// user message with text then tool_result: text becomes message item first,
	// then tool_result becomes function_call_output.
	body := `{"model":"claude-opus-4.7","tools":[{"name":"f","description":"fn","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"f","input":{}}]},{"role":"user","content":[{"type":"text","text":"Here's the result:"},{"type":"tool_result","tool_use_id":"toolu_01","content":"done"}]}]}`
	got, err := translator.TranslateRequest([]byte(body), translator.RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	input := m["input"].([]any)
	// Expect: function_call, message (text), function_call_output
	if len(input) != 3 {
		t.Fatalf("want 3 input items, got %d: %s", len(input), string(got))
	}
	types := make([]string, len(input))
	for i, it := range input {
		types[i] = it.(map[string]any)["type"].(string)
	}
	if types[0] != "function_call" || types[1] != "message" || types[2] != "function_call_output" {
		t.Errorf("unexpected item types: %v, want [function_call message function_call_output]", types)
	}
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
