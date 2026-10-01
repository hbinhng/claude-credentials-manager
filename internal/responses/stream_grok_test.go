package responses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/responses"
)

// grokSSE builds a grok-shell-shaped Responses stream from data payloads.
func grokSSE(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

type sseEvent struct {
	Name string
	Data map[string]any
}

func runStream(t *testing.T, opts responses.StreamOpts, in string) []sseEvent {
	t.Helper()
	var out bytes.Buffer
	if err := responses.NewStreamTranslator(opts).Pipe(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	var evs []sseEvent
	var name string
	for _, line := range strings.Split(out.String(), "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var d map[string]any
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d)
			evs = append(evs, sseEvent{name, d})
		}
	}
	return evs
}

// reasoningTwoParts is one reasoning item with two summary parts, then a
// text message, then response.completed.
var reasoningTwoParts = grokSSE(
	`{"type":"response.created","response":{"id":"r1"}}`,
	`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
	`{"type":"response.reasoning_summary_part.added"}`,
	`{"type":"response.reasoning_summary_text.delta","delta":"first"}`,
	`{"type":"response.reasoning_summary_text.done"}`,
	`{"type":"response.reasoning_summary_part.done"}`,
	`{"type":"response.reasoning_summary_part.added"}`,
	`{"type":"response.reasoning_summary_text.delta","delta":"second"}`,
	`{"type":"response.reasoning_summary_text.done"}`,
	`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"first"},{"type":"summary_text","text":"second"}],"encrypted_content":"ENC"}}`,
	`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1"}}`,
	`{"type":"response.output_text.delta","delta":"pong"}`,
	`{"type":"response.output_text.done"}`,
	`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1"}}`,
	`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":20410,"output_tokens":68,"input_tokens_details":{"cached_tokens":20224}}}}`,
)

func TestGrokStream_OneSignedThinkingBlockPerReasoningItem(t *testing.T) {
	evs := runStream(t, responses.StreamOpts{Model: "m", Dialect: responses.Grok}, reasoningTwoParts)
	var starts, stops int
	var thinking strings.Builder
	var sig string
	sigBeforeStop := false
	for i, e := range evs {
		switch e.Name {
		case "content_block_start":
			starts++
		case "content_block_stop":
			stops++
		case "content_block_delta":
			d := e.Data["delta"].(map[string]any)
			switch d["type"] {
			case "thinking_delta":
				thinking.WriteString(d["thinking"].(string))
			case "signature_delta":
				sig = d["signature"].(string)
				sigBeforeStop = i+1 < len(evs) && evs[i+1].Name == "content_block_stop"
			}
		}
	}
	if starts != 2 || stops != 2 {
		t.Errorf("starts/stops = %d/%d, want 2/2 (one thinking + one text)", starts, stops)
	}
	if thinking.String() != "first\n\nsecond" {
		t.Errorf("thinking = %q, want parts joined by blank line", thinking.String())
	}
	if !strings.HasPrefix(sig, "ccmrs1.") || !sigBeforeStop {
		t.Errorf("signature %q (immediately before stop: %v)", sig, sigBeforeStop)
	}
	// The signature decodes back into the same reasoning item on the next request.
	body := `{"model":"x","messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + sig + `"}]}]}`
	out, err := responses.TranslateRequest([]byte(body), responses.RequestOpts{TargetModel: "m", Dialect: responses.Grok})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"encrypted_content":"ENC"`) || !strings.Contains(string(out), `"id":"rs_1"`) {
		t.Errorf("round-trip lost the reasoning item: %s", out)
	}
}

func TestGrokStream_OmitThinkingTextKeepsSignature(t *testing.T) {
	evs := runStream(t, responses.StreamOpts{Model: "m", Dialect: responses.Grok, OmitThinkingText: true}, reasoningTwoParts)
	for _, e := range evs {
		if e.Name == "content_block_delta" && e.Data["delta"].(map[string]any)["type"] == "thinking_delta" {
			t.Fatalf("thinking_delta emitted under OmitThinkingText: %v", e.Data)
		}
	}
	found := false
	for _, e := range evs {
		if e.Name == "content_block_delta" && e.Data["delta"].(map[string]any)["type"] == "signature_delta" {
			found = true
		}
	}
	if !found {
		t.Error("signature_delta missing under OmitThinkingText")
	}
}

func TestGrokStream_ReasoningWithoutEncryptedContentClosesUnsigned(t *testing.T) {
	in := grokSSE(
		`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.completed","response":{"id":"r1"}}`,
	)
	evs := runStream(t, responses.StreamOpts{Model: "m", Dialect: responses.Grok}, in)
	for _, e := range evs {
		if e.Name == "content_block_delta" {
			t.Errorf("unexpected delta %v", e.Data)
		}
	}
}

func TestGrokStream_UnknownOutputItemSkipped(t *testing.T) {
	in := grokSSE(
		`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.output_item.added","item":{"type":"compaction","id":"c1"}}`,
		`{"type":"response.output_item.done","item":{"type":"compaction","id":"c1"}}`,
		`{"type":"response.output_item.added","item":{"type":"message","id":"m1"}}`,
		`{"type":"response.output_text.delta","delta":"ok"}`,
		`{"type":"response.output_text.done"}`,
		`{"type":"response.completed","response":{"id":"r1"}}`,
	)
	evs := runStream(t, responses.StreamOpts{Model: "m", Dialect: responses.Grok}, in)
	starts := 0
	for _, e := range evs {
		if e.Name == "content_block_start" {
			starts++
			if e.Data["index"].(float64) != 0 {
				t.Errorf("text block index = %v, want 0 (unknown item consumed no index)", e.Data["index"])
			}
		}
	}
	if starts != 1 {
		t.Errorf("starts = %d, want 1", starts)
	}
}

func TestGrokStream_FunctionCallAndUsage(t *testing.T) {
	in := grokSSE(
		`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call-1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"command\":\"ls\"}"}`,
		`{"type":"response.function_call_arguments.done"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1"}}`,
		`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":20126,"output_tokens":156}}}`,
	)
	st := responses.NewStreamTranslator(responses.StreamOpts{Model: "m", Dialect: responses.Grok})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"stop_reason":"tool_use"`) {
		t.Errorf("stop_reason tool_use missing: %s", out.String())
	}
	if st.FinalUsage().InputTokens != 20126 {
		t.Errorf("FinalUsage.InputTokens = %d", st.FinalUsage().InputTokens)
	}
}

func TestCodexStream_ThinkingUnchangedWithoutCarry(t *testing.T) {
	evs := runStream(t, responses.StreamOpts{Model: "m"}, reasoningTwoParts)
	thinkingStarts := 0
	for _, e := range evs {
		if e.Name == "content_block_start" && e.Data["content_block"].(map[string]any)["type"] == "thinking" {
			thinkingStarts++
		}
		if e.Name == "content_block_delta" && e.Data["delta"].(map[string]any)["type"] == "signature_delta" {
			t.Error("codex must not emit signatures")
		}
	}
	if thinkingStarts != 2 {
		t.Errorf("codex thinking blocks = %d, want 2 (one per summary part, as before)", thinkingStarts)
	}
}

func TestGrokStream_OmitThinkingTextSuppressesReasoningTextDelta(t *testing.T) {
	in := grokSSE(
		`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_text.delta","delta":"hidden"}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.completed","response":{"id":"r1"}}`,
	)
	for _, e := range runStream(t, responses.StreamOpts{Model: "m", Dialect: responses.Grok, OmitThinkingText: true}, in) {
		if e.Name == "content_block_delta" {
			t.Errorf("unexpected delta %v", e.Data)
		}
	}
}

func TestThinkingOmitted(t *testing.T) {
	for body, want := range map[string]bool{
		`{"thinking":{"type":"adaptive","display":"omitted"}}`: true,
		`{"thinking":{"type":"adaptive"}}`:                     false,
		`{"model":"x"}`:                                        false,
		`not json`:                                             false,
	} {
		if got := responses.ThinkingOmitted([]byte(body)); got != want {
			t.Errorf("ThinkingOmitted(%s) = %v, want %v", body, got, want)
		}
	}
}
