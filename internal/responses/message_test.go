package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func assembleFrom(t *testing.T, opts StreamOpts, upstream string) map[string]any {
	t.Helper()
	sse, err := NewStreamTranslator(opts).Collect(context.Background(), strings.NewReader(upstream))
	if err != nil {
		t.Fatal(err)
	}
	js, err := AssembleMessage(sse)
	if err != nil {
		t.Fatalf("AssembleMessage: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(js, &m); err != nil {
		t.Fatalf("not JSON: %s", js)
	}
	return m
}

func up(events ...string) string {
	var b bytes.Buffer
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

func TestAssembleMessage_TextThinkingToolAndUsage(t *testing.T) {
	m := assembleFrom(t, StreamOpts{Model: "claude-x", Dialect: Grok}, up(
		`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_part.added"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"hm"}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"E"}}`,
		`{"type":"response.output_item.added","item":{"type":"message","id":"m1"}}`,
		`{"type":"response.output_text.delta","delta":"po"}`,
		`{"type":"response.output_text.delta","delta":"ng"}`,
		`{"type":"response.output_text.done"}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc","call_id":"call-1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"command\":"}`,
		`{"type":"response.function_call_arguments.delta","delta":"\"ls\"}"}`,
		`{"type":"response.function_call_arguments.done"}`,
		`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":3}}}`,
	))
	if m["type"] != "message" || m["role"] != "assistant" || m["model"] != "claude-x" || m["stop_reason"] != "tool_use" {
		t.Errorf("envelope = %v", m)
	}
	c := m["content"].([]any)
	if len(c) != 3 {
		t.Fatalf("content = %v", c)
	}
	th := c[0].(map[string]any)
	if th["type"] != "thinking" || th["thinking"] != "hm" || !strings.HasPrefix(th["signature"].(string), "ccmrs1.") {
		t.Errorf("thinking block = %v", th)
	}
	if tx := c[1].(map[string]any); tx["text"] != "pong" {
		t.Errorf("text block = %v", tx)
	}
	tu := c[2].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "call-1" || tu["input"].(map[string]any)["command"] != "ls" {
		t.Errorf("tool_use block = %v", tu)
	}
	if _, leaked := tu["_partial"]; leaked {
		t.Error("internal _partial key leaked")
	}
	if u := m["usage"].(map[string]any); u["input_tokens"].(float64) != 7 {
		t.Errorf("usage = %v", u)
	}
}

func TestAssembleMessage_ErrorEventReturnedVerbatim(t *testing.T) {
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\"}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"boom\"}}\n\n"
	js, err := AssembleMessage([]byte(sse))
	if err != nil || !strings.Contains(string(js), `"boom"`) {
		t.Errorf("js=%s err=%v", js, err)
	}
}

func TestAssembleMessage_NoMessage(t *testing.T) {
	if _, err := AssembleMessage([]byte(`{"type":"error"}`)); !errors.Is(err, ErrNoMessage) {
		t.Errorf("err = %v, want ErrNoMessage", err)
	}
}

func TestAssembleMessage_IgnoresJunkAndOutOfRangeIndexes(t *testing.T) {
	sse := "data: not json\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"early\"}}\n\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":5,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":5}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"input\":{}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{bad\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	js, err := AssembleMessage([]byte(sse))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(js, &m)
	c := m["content"].([]any)
	if len(c) != 1 || c[0].(map[string]any)["type"] != "tool_use" {
		t.Errorf("content = %v (nil slot at index 0 must be skipped; bad partial JSON keeps input {})", c)
	}
	if m["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", m["stop_reason"])
	}
}

func TestAssembleMessage_LineTooLong(t *testing.T) {
	prev := assembleMaxLine
	assembleMaxLine = 64
	defer func() { assembleMaxLine = prev }()
	if _, err := AssembleMessage([]byte("data: " + strings.Repeat("x", 200) + "\n")); err == nil {
		t.Error("want scanner error for an over-long line")
	}
}
