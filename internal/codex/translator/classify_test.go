package translator_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

func TestClassifyStream_Fallthrough_EOF(t *testing.T) {
	// Upstream closes before emitting any decisive event. Classifier
	// must return non-overflow with nil error so the caller can fall
	// through to streaming and let the translator's EOF safety net
	// handle whatever's left.
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.in_progress","response":{"id":"r1"}}`,
		``,
	}, "\n")
	dec, replay, rem, err := translator.ClassifyStream(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("err = %v, want nil on EOF", err)
	}
	if dec.Overflow {
		t.Errorf("Overflow = true, want false on EOF fallthrough")
	}
	rest, _ := io.ReadAll(rem)
	got := append([]byte{}, replay...)
	got = append(got, rest...)
	if !bytes.Equal(got, []byte(input)) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, input)
	}
}

func TestClassifyStream_Fallthrough_Timeout(t *testing.T) {
	// The deadline check at the top of ClassifyStream's loop fires
	// between iterations, not mid-ReadString. To exercise it, write
	// one event, sleep past the deadline, then write a second event:
	//   t=0    : ReadString returns line 1
	//   t<1ms  : classifier processes (non-decisive), loops back, blocks in ReadString
	//   t=80ms : line 2 written, ReadString unblocks, classifier processes line 2, loops
	//   t=81ms : deadline check (50ms deadline crossed at 50ms) → returns via timeout branch
	t.Cleanup(func() { translator.SetClassifyFirstByteTimeoutForTest(30 * time.Second) })
	translator.SetClassifyFirstByteTimeoutForTest(50 * time.Millisecond)

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\"}\n\n"))
		time.Sleep(80 * time.Millisecond)
		_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\"}\n\n"))
		// Leave the pipe open so ReadString would block again if the
		// deadline check failed to fire. The test will hang if the
		// timeout branch is broken.
	}()
	defer pw.Close()
	defer pr.Close()

	done := make(chan struct{})
	var (
		dec    translator.StreamDecision
		gotErr error
	)
	go func() {
		dec, _, _, gotErr = translator.ClassifyStream(context.Background(), pr)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("ClassifyStream did not return within 2s — timeout branch likely broken")
	}
	if gotErr != nil {
		t.Errorf("err = %v, want nil on timeout fallthrough", gotErr)
	}
	if dec.Overflow {
		t.Errorf("Overflow = true, want false on timeout fallthrough")
	}
}

func TestClassifyStream_ReadError(t *testing.T) {
	// A non-EOF read error from upstream (e.g., broken connection)
	// must propagate to the caller so the terminal can surface it
	// as a 502, rather than being mis-classified as overflow or
	// silently falling through to streaming.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"))
		_ = pw.CloseWithError(errors.New("upstream broken pipe"))
	}()

	dec, _, _, err := translator.ClassifyStream(context.Background(), pr)
	if err == nil {
		t.Fatalf("err = nil, want non-EOF read error to propagate")
	}
	if dec.Overflow {
		t.Errorf("Overflow = true, want false on read error")
	}
}

func TestClassifyStream_ContextCancel(t *testing.T) {
	// Cancel the context while the reader is blocked. Classifier must
	// return the ctx error so the caller can surface a 502 (or similar).
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before starting so the first ctx check fires

	dec, _, _, err := translator.ClassifyStream(ctx, pr)
	if err == nil {
		t.Fatalf("err = nil, want context.Canceled")
	}
	if dec.Overflow {
		t.Errorf("Overflow = true, want false when ctx is canceled")
	}
}
