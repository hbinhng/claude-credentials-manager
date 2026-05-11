// Internal white-box tests for branches that require accessing package-level
// variables (collectMaxBytes) or helpers not exported to external test code.
package translator

import (
	"bytes"
	"context"
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
