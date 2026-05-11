package translator_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/translator"
)

// helper: feed SSE lines through ClassifyStream and assert replay+remaining round-trip.
func runClassify(t *testing.T, input string) (translator.StreamDecision, []byte, io.Reader) {
	t.Helper()
	dec, replay, rem, err := translator.ClassifyStream(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("ClassifyStream: %v", err)
	}
	// Round-trip: replay + readAll(remaining) must equal input.
	rest, _ := io.ReadAll(rem)
	got := append([]byte{}, replay...)
	got = append(got, rest...)
	if !bytes.Equal(got, []byte(input)) {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", got, input)
	}
	return dec, replay, nil
}

func TestClassifyStream_NotOverflow_TextDeltaThenIncomplete(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":100,"output_tokens":50}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false (text delta arrived before incomplete)")
	}
}

func TestClassifyStream_NotOverflow_ToolArgsDeltaThenIncomplete(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc1","call_id":"c1","name":"f"}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","delta":"{}"}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"max_output_tokens"}}}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false (function args delta arrived before incomplete)")
	}
}

func TestClassifyStream_NotOverflow_Completed(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		``,
		`data: {"type":"response.completed","status":"completed","response":{"id":"r1","usage":{"input_tokens":10,"output_tokens":2}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false (completed cleanly)")
	}
}

func TestClassifyStream_NotOverflow_Failed(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.failed","response":{"id":"r1","error":{"code":"server_overloaded","message":"overloaded"}}}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false (failure is its own class)")
	}
}

func TestClassifyStream_OverflowEmptyReasoningThenIncomplete(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.in_progress","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs1","summary":[]}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs1","summary":[]}}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":271392,"output_tokens":160}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	dec, _, _ := runClassify(t, input)
	if !dec.Overflow {
		t.Fatalf("Overflow = false, want true")
	}
	if dec.InputTokens != 271392 {
		t.Errorf("InputTokens = %d, want 271392", dec.InputTokens)
	}
	if dec.Limit != 271552 {
		t.Errorf("Limit = %d, want 271552 (input+output)", dec.Limit)
	}
}

func TestClassifyStream_Overflow_SummaryDeltasOnly(t *testing.T) {
	// Reasoning summary text deltas are NOT actionable per the design.
	// Even if 100 of them arrive, an incomplete{max_output_tokens} that
	// follows still means the user got zero actionable output.
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs1","summary":[]}}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking..."}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","delta":" more"}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":271392,"output_tokens":160}}}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if !dec.Overflow {
		t.Errorf("Overflow = false, want true (summary deltas are not actionable)")
	}
	if dec.InputTokens != 271392 || dec.Limit != 271552 {
		t.Errorf("got InputTokens=%d Limit=%d, want 271392 / 271552", dec.InputTokens, dec.Limit)
	}
}

func TestClassifyStream_Overflow_ReasoningTextDeltasOnly(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.reasoning_text.delta","delta":"hmm"}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":50,"output_tokens":10}}}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if !dec.Overflow {
		t.Errorf("Overflow = false, want true (reasoning_text.delta is not actionable)")
	}
}

func TestClassifyStream_Overflow_IncompleteWithoutUsage(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"max_output_tokens"}}}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if !dec.Overflow {
		t.Errorf("Overflow = false, want true (missing usage still classifies as overflow)")
	}
	if dec.InputTokens != 0 || dec.Limit != 0 {
		t.Errorf("got InputTokens=%d Limit=%d, want 0/0 fallback", dec.InputTokens, dec.Limit)
	}
}

func TestClassifyStream_NotOverflow_OtherIncompleteReason(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"content_filter"}}}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false (content_filter is not max_output_tokens)")
	}
}

func TestClassifyStream_NotOverflow_IncompleteNoDetails(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.incomplete","response":{"id":"r1"}}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false (no incomplete_details means reason is unknown, not max_output_tokens)")
	}
}

func TestClassifyStream_MalformedLineSkipped(t *testing.T) {
	// A malformed data: line must be skipped without disturbing the
	// decision flow. The text delta after the garbage should still be
	// detected.
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: this is not json`,
		``,
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false (text delta seen after malformed line)")
	}
}

func TestClassifyStream_DoneMarkerSkipped(t *testing.T) {
	// data: [DONE] and empty-payload data: lines must skip cleanly
	// without disturbing classification. Sequence: non-decisive
	// event, then [DONE], then EOF — classifier should fall through
	// to non-overflow without error.
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: `,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	dec, _, _ := runClassify(t, input)
	if dec.Overflow {
		t.Errorf("Overflow = true, want false ([DONE] marker followed by EOF)")
	}
}
