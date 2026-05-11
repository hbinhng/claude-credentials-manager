// Internal white-box tests for branches that require accessing package-level
// variables (collectMaxBytes) or helpers not exported to external test code.
package translator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCollect_ExceedsCapReturnsError verifies that Collect returns an error
// JSON envelope when the buffered output exceeds collectMaxBytes.
func TestCollect_ExceedsCapReturnsError(t *testing.T) {
	// Temporarily lower the cap so we don't need to generate 64 MiB.
	original := collectMaxBytes
	collectMaxBytes = 10
	defer func() { collectMaxBytes = original }()

	// A minimal stream that will produce more than 10 bytes of output.
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := NewStreamTranslator(StreamOpts{MessageID: "msg_test", Model: "m"})
	out, err := st.Collect(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("Collect returned unexpected error: %v", err)
	}
	// The returned bytes should be a JSON error envelope, not SSE.
	if !bytes.Contains(out, []byte(`"error"`)) {
		t.Errorf("expected error envelope in output, got: %s", out)
	}
}

// TestWriteSSE_SecondWriteError verifies that writeSSE returns an error when
// the second Write call (for the data: line) fails.
func TestWriteSSE_SecondWriteError(t *testing.T) {
	w := &secondWriteErrorWriter{}
	err := writeSSE(w, "event_name", "payload")
	if err == nil {
		t.Error("expected error from second Write, got nil")
	}
}

// secondWriteErrorWriter fails on the second call to Write.
type secondWriteErrorWriter struct {
	calls int
}

func (w *secondWriteErrorWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= 2 {
		return 0, bytes.ErrTooLarge
	}
	return len(p), nil
}

func TestTruncateAtWordBoundary_Short(t *testing.T) {
	if got := truncateAtWordBoundary("hello", 100); got != "hello" {
		t.Errorf("short input should pass through, got %q", got)
	}
}

func TestTruncateAtWordBoundary_CutAtLastSpace(t *testing.T) {
	in := "hello there friend"
	got := truncateAtWordBoundary(in, 12)
	if got != "hello there" {
		t.Errorf("truncate(12) = %q, want \"hello there\"", got)
	}
}

func TestTruncateAtWordBoundary_NoSpaceFallsBackToHardCut(t *testing.T) {
	in := "abcdefghij"
	got := truncateAtWordBoundary(in, 5)
	if got != "abcde" {
		t.Errorf("no-space input truncate(5) = %q, want \"abcde\"", got)
	}
}

func TestTruncateAtWordBoundary_PreservesUTF8AfterHardCut(t *testing.T) {
	// 5 ASCII bytes + 3-byte CJK rune = 8 bytes; max=7 lands inside the rune.
	in := "abcde" + "\xe7\x95\x8c"
	if utf8.RuneCountInString(in) != 6 {
		t.Fatalf("test setup wrong: rune count = %d", utf8.RuneCountInString(in))
	}
	got := truncateAtWordBoundary(in, 7)
	if !utf8.ValidString(got) {
		t.Errorf("truncated output is invalid UTF-8: %q", got)
	}
	// Expected behavior: back up to the rune boundary at byte 5.
	if got != "abcde" {
		t.Errorf("truncate(7) = %q, want \"abcde\"", got)
	}
}

func TestTruncateAtWordBoundary_LeadingWhitespaceOnlyDropsToEmpty(t *testing.T) {
	// String starts with whitespace at byte 0, no other whitespace.
	in := "\nabcdefghij"
	got := truncateAtWordBoundary(in, 5)
	// lastWhitespaceIndex returns 0; with the i>=0 guard, cut[:0] = "".
	if got != "" {
		t.Errorf("truncate(5) on leading-whitespace-only input = %q, want empty (whitespace boundary at index 0)", got)
	}
}

func TestMapIncompleteReason(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"max_output_tokens", "max_tokens"},
		{"content_filter", "refusal"},
		{"", "end_turn"},
		{"unknown", "end_turn"},
		{"some_future_reason_we_dont_know_yet", "end_turn"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapIncompleteReason(tc.in); got != tc.want {
				t.Errorf("mapIncompleteReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapFailedCode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"context_length_exceeded", "request_too_large"},
		{"insufficient_quota", "rate_limit_error"},
		{"rate_limit_exceeded", "rate_limit_error"},
		{"server_overloaded", "overloaded_error"},
		{"invalid_prompt", "invalid_request_error"},
		{"cyber_policy", "invalid_request_error"},
		{"", "api_error"},
		{"some_other_code", "api_error"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapFailedCode(tc.in); got != tc.want {
				t.Errorf("mapFailedCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFinalize_Idempotent verifies that finalize returns nil and
// emits no events when the message has already been ended. This
// guards against double-finalization when multiple terminal codex
// events (e.g. response.failed followed by upstream-truncated bytes
// that trip the EOF safety net) target the same stream.
func TestFinalize_Idempotent(t *testing.T) {
	st := NewStreamTranslator(StreamOpts{MessageID: "m", Model: "m"})
	st.messageEnded = true
	if got := st.finalize("end_turn", nil); got != nil {
		t.Errorf("expected nil from finalize on already-ended message, got %v", got)
	}
}

// TestApply_OutputItemDone_ClosesOpenBlock verifies that
// response.output_item.done emits a content_block_stop when a block
// is still open (the captured-bug scenario: empty reasoning summary,
// no inner _text.done to close the block). When no block is open
// (the common case: inner _text.done already closed it), the event
// emits nothing.
// TestApply_Failed_EmptyErrorFallback verifies that response.failed
// with no error object still produces a well-formed terminal close
// using a generic "codex upstream returned response.failed" message.
func TestApply_Failed_EmptyErrorFallback(t *testing.T) {
	tr := NewStreamTranslator(StreamOpts{MessageID: "msg_x", Model: "m"})
	_ = tr.apply(codexEvent{Type: "response.created", Response: &codexResponseInfo{ID: "resp_x"}})
	em := tr.apply(codexEvent{Type: "response.failed"})

	// Expect: [error, message_delta, message_stop]. No content_block_stop
	// because no block was open.
	if len(em) != 3 {
		t.Fatalf("expected 3 emissions, got %d: %+v", len(em), em)
	}
	if em[0].name != "error" {
		t.Errorf("emission 0: want error, got %q", em[0].name)
	}
	if !strings.Contains(em[0].data, `"type":"api_error"`) {
		t.Errorf("emission 0 should map to api_error, got %s", em[0].data)
	}
	if !strings.Contains(em[0].data, "codex upstream returned response.failed") {
		t.Errorf("emission 0 should carry fallback message, got %s", em[0].data)
	}
	if em[1].name != "message_delta" || em[2].name != "message_stop" {
		t.Errorf("expected message_delta then message_stop, got %q then %q", em[1].name, em[2].name)
	}
	if !tr.messageEnded {
		t.Error("messageEnded should be true after response.failed")
	}
}

func TestApply_OutputItemDone_ClosesOpenBlock(t *testing.T) {
	t.Run("open_block_is_closed", func(t *testing.T) {
		tr := NewStreamTranslator(StreamOpts{MessageID: "msg_x", Model: "m"})
		// Set up: message_start emitted, reasoning block open.
		_ = tr.apply(codexEvent{Type: "response.created", Response: &codexResponseInfo{ID: "resp_x"}})
		_ = tr.apply(codexEvent{
			Type: "response.output_item.added",
			Item: &codexOutputItem{Type: "reasoning", ID: "rs_x"},
		})
		if tr.currentBlockIdx < 0 {
			t.Fatalf("setup failed: expected open block")
		}
		em := tr.apply(codexEvent{Type: "response.output_item.done"})
		if len(em) != 1 || em[0].name != "content_block_stop" {
			t.Fatalf("expected [content_block_stop], got %+v", em)
		}
		if tr.currentBlockIdx >= 0 {
			t.Errorf("block should be closed, currentBlockIdx=%d", tr.currentBlockIdx)
		}
	})

	t.Run("no_open_block_emits_nothing", func(t *testing.T) {
		tr := NewStreamTranslator(StreamOpts{MessageID: "msg_x", Model: "m"})
		// Setup with a block that's been opened and closed normally.
		_ = tr.apply(codexEvent{Type: "response.created", Response: &codexResponseInfo{ID: "resp_x"}})
		_ = tr.apply(codexEvent{
			Type: "response.output_item.added",
			Item: &codexOutputItem{Type: "message", ID: "msg_x"},
		})
		_ = tr.apply(codexEvent{Type: "response.output_text.done"}) // closes the block
		if tr.currentBlockIdx >= 0 {
			t.Fatalf("setup failed: block should be closed")
		}
		em := tr.apply(codexEvent{Type: "response.output_item.done"})
		if len(em) != 0 {
			t.Errorf("expected no emissions when no block open, got %+v", em)
		}
	})
}

// TestApply_ReasoningTextDelta_InsideReasoningBlock verifies that
// response.reasoning_text.delta (the non-summary variant in codex-rs
// vocabulary) is treated as a thinking_delta when a reasoning block
// is open, and dropped otherwise.
func TestApply_ReasoningTextDelta_InsideReasoningBlock(t *testing.T) {
	t.Run("inside_reasoning_block_emits_thinking_delta", func(t *testing.T) {
		tr := NewStreamTranslator(StreamOpts{MessageID: "msg_x", Model: "m"})
		_ = tr.apply(codexEvent{Type: "response.created", Response: &codexResponseInfo{ID: "resp_x"}})
		_ = tr.apply(codexEvent{
			Type: "response.output_item.added",
			Item: &codexOutputItem{Type: "reasoning", ID: "rs_x"},
		})
		em := tr.apply(codexEvent{Type: "response.reasoning_text.delta", Delta: "hmm"})
		if len(em) != 1 || em[0].name != "content_block_delta" {
			t.Fatalf("expected [content_block_delta], got %+v", em)
		}
		if !strings.Contains(em[0].data, `"thinking_delta"`) {
			t.Errorf("expected thinking_delta payload, got %s", em[0].data)
		}
		if !strings.Contains(em[0].data, `"thinking":"hmm"`) {
			t.Errorf("expected delta text to be carried, got %s", em[0].data)
		}
	})

	t.Run("outside_reasoning_block_drops_silently", func(t *testing.T) {
		tr := NewStreamTranslator(StreamOpts{MessageID: "msg_x", Model: "m"})
		_ = tr.apply(codexEvent{Type: "response.created", Response: &codexResponseInfo{ID: "resp_x"}})
		_ = tr.apply(codexEvent{
			Type: "response.output_item.added",
			Item: &codexOutputItem{Type: "message", ID: "msg_x"},
		})
		em := tr.apply(codexEvent{Type: "response.reasoning_text.delta", Delta: "should not appear"})
		if len(em) != 0 {
			t.Errorf("expected no emissions when not in reasoning block, got %+v", em)
		}
	})
}

// nthWriteErrorWriter succeeds for the first N-1 writes, then fails on write N.
type nthWriteErrorWriter struct {
	failAt int // Write index (1-based) at which to fail.
	calls  int
}

func (w *nthWriteErrorWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAt {
		return 0, bytes.ErrTooLarge
	}
	return len(p), nil
}

// TestRun_EOFSafetyNet_WriteSSEError verifies that when the safety
// net's writeSSE loop encounters a write error mid-finalize, run()
// returns that error (rather than swallowing it). Sets up a stream
// that opens a message_start (passing several writes successfully)
// then ends with [DONE] before any terminal event, so the safety
// net fires; uses a writer that fails partway through the safety-
// net emissions to hit the `return err` branch.
func TestRun_EOFSafetyNet_WriteSSEError(t *testing.T) {
	// writeSSE makes 2 io.WriteString calls per emission:
	//   write 1: "event: <name>\n"
	//   write 2: "data: <payload>\n\n"
	//
	// Apply-loop emissions before [DONE]:
	//   response.created        → message_start        → writes 1-2
	//   response.output_item.added → content_block_start → writes 3-4
	//
	// Safety-net emissions (finalize with open block):
	//   content_block_stop  → writes 5-6  (first safety-net emission)
	//   message_delta       → writes 7-8
	//   message_stop        → writes 9-10
	//
	// failAt=5 fails on write 5 — the event line of content_block_stop,
	// the very first write the safety net attempts. This is the earliest
	// value that exercises `return err` inside the safety-net loop while
	// still letting messageStarted become true (writes 1-4 succeed).
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_eof_err"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_eof_err"}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	tr := NewStreamTranslator(StreamOpts{MessageID: "m", Model: "x"})
	w := &nthWriteErrorWriter{failAt: 5}
	err := tr.Pipe(context.Background(), strings.NewReader(input), w)
	if err == nil {
		t.Fatal("expected writeSSE error to propagate from safety net, got nil")
	}
	if !errors.Is(err, bytes.ErrTooLarge) {
		t.Errorf("expected bytes.ErrTooLarge, got %v", err)
	}
}
