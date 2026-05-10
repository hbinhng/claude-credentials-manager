package translator

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestLoopReplay_DroppedToolsNeverInOutboundTools replays each
// inbound /v1/messages from the fixture through TranslateRequest and
// asserts that NO outbound tools[] entry has a name in
// droppedClaudeTools. This is the post-Phase-2 regression assertion:
// any future change that re-enables a Claude-Code-only tool will fail
// here.
func TestLoopReplay_DroppedToolsNeverInOutboundTools(t *testing.T) {
	for i, body := range loadFixtureBodies(t) {
		out, err := TranslateRequest(body, RequestOpts{TargetModel: "gpt-5"})
		if err != nil {
			t.Fatalf("turn %d: TranslateRequest: %v", i, err)
		}
		var probe struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("turn %d: unmarshal: %v", i, err)
		}
		for _, tt := range probe.Tools {
			if isDroppedClaudeTool(tt.Name) {
				t.Errorf("turn %d: outbound tools[] contains dropped tool %q", i, tt.Name)
			}
		}
	}
}

// TestLoopReplay_StaleReminderNeverInOutboundInput replays each
// inbound and asserts that the outbound input[] does NOT contain the
// stale "task tools haven't been used recently" reminder text. This is
// the post-Phase-3 regression assertion.
func TestLoopReplay_StaleReminderNeverInOutboundInput(t *testing.T) {
	needle := []byte("haven't been used recently")
	for i, body := range loadFixtureBodies(t) {
		out, err := TranslateRequest(body, RequestOpts{TargetModel: "gpt-5"})
		if err != nil {
			t.Fatalf("turn %d: TranslateRequest: %v", i, err)
		}
		if bytes.Contains(out, needle) {
			t.Errorf("turn %d: outbound body still contains stale reminder text", i)
		}
	}
}

// TestLoopReplay_NoToolResultExceeds64KB asserts the Phase 9
// truncation cap is enforced: every function_call_output.output in
// the outbound input[] is <= toolResultMaxBytes.
func TestLoopReplay_NoToolResultExceeds64KB(t *testing.T) {
	for i, body := range loadFixtureBodies(t) {
		out, err := TranslateRequest(body, RequestOpts{TargetModel: "gpt-5"})
		if err != nil {
			t.Fatalf("turn %d: TranslateRequest: %v", i, err)
		}
		var probe struct {
			Input []struct {
				Type   string `json:"type"`
				Output string `json:"output,omitempty"`
			} `json:"input"`
		}
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("turn %d: unmarshal: %v", i, err)
		}
		for _, it := range probe.Input {
			if it.Type == "function_call_output" && len(it.Output) > toolResultMaxBytes {
				t.Errorf("turn %d: tool_result %d bytes exceeds cap %d",
					i, len(it.Output), toolResultMaxBytes)
			}
		}
	}
}

// loadFixtureBodies extracts the in.raw /v1/messages bodies from the
// fixture file. Returns them in trace-recording order.
func loadFixtureBodies(t *testing.T) [][]byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/loop_replay/sample.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var bodies [][]byte
	for _, ln := range strings.Split(string(raw), "\n") {
		if len(ln) == 0 || ln[0] != '{' {
			continue
		}
		var l struct {
			Dir  string `json:"dir"`
			URL  string `json:"url"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			continue
		}
		if l.Dir == "in.raw" && l.URL == "/v1/messages" {
			bodies = append(bodies, []byte(l.Body))
		}
	}
	if len(bodies) < 5 {
		t.Fatalf("fixture has only %d inbound bodies; expected ≥5", len(bodies))
	}
	return bodies
}
