// Package translator_test exercises StreamTranslator via fixture pairs under
// testdata/stream/. Each *.codex.txt file is a sequence of codex SSE events;
// the corresponding *.anthropic.txt is the expected Anthropic SSE output.
//
// Snapshot workflow: if you change the translator logic, delete the
// *.anthropic.txt files, run the tests once (they will fail and print the
// actual output), then copy each "GOT:" block into the corresponding
// *.anthropic.txt file and re-run. Verify the output is semantically correct.
package translator_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/translator"
)

// TestStreamTranslator_Pipe_Fixtures drives every *.codex.txt file in
// testdata/stream/ through Pipe and compares the output to the paired
// *.anthropic.txt file under text-line normalization.
func TestStreamTranslator_Pipe_Fixtures(t *testing.T) {
	dir := "testdata/stream"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".codex.txt") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".codex.txt")
		t.Run(base, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join(dir, base+".codex.txt"))
			if err != nil {
				t.Fatalf("read codex fixture: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(dir, base+".anthropic.txt"))
			if err != nil {
				t.Fatalf("read anthropic fixture: %v", err)
			}

			st := translator.NewStreamTranslator(translator.StreamOpts{
				MessageID: "msg_test",
				Model:     "claude-opus-4.7",
			})
			var got bytes.Buffer
			if err := st.Pipe(context.Background(), bytes.NewReader(in), &got); err != nil {
				t.Fatalf("Pipe: %v", err)
			}
			if normalize(got.String()) != normalize(string(want)) {
				t.Errorf("mismatch:\nGOT:\n%s\nWANT:\n%s", got.String(), string(want))
			}
		})
	}
}

// TestStreamTranslator_ContextCancel verifies that a cancelled context
// causes Pipe to return ctx.Err() promptly even when the reader blocks.
func TestStreamTranslator_ContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "x", Model: "y"})
	ctx, cancel := context.WithCancel(context.Background())

	// Feed one line so the scanner has something to scan; we need the loop
	// to start and then be cancelled before it can advance.
	done := make(chan error, 1)
	go func() {
		done <- st.Pipe(ctx, pr, &bytes.Buffer{})
	}()

	// Cancel the context and then write something so the scanner can unblock.
	cancel()
	// Write a line to unblock the scanner's internal Read.
	_, _ = pw.Write([]byte("data: [DONE]\n\n"))

	err := <-done
	if err == nil {
		// It's possible the [DONE] was processed before the cancel check;
		// that's fine as long as we don't panic. The cancel check is
		// best-effort at loop-top.
		// Re-run with a guaranteed-blocking reader to exercise the path.
		st2 := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "x", Model: "y"})
		ctx2, cancel2 := context.WithCancel(context.Background())
		cancel2() // cancel before starting
		pr2, pw2 := io.Pipe()
		defer pw2.Close()
		defer pr2.Close()
		if err2 := st2.Pipe(ctx2, pr2, &bytes.Buffer{}); err2 == nil {
			t.Error("expected ctx cancel error, got nil")
		}
	}
}

// TestStreamTranslator_FinalUsage verifies that FinalUsage returns the
// usage from the last response.completed event.
func TestStreamTranslator_FinalUsage(t *testing.T) {
	in, err := os.ReadFile("testdata/stream/usage-with-cached.codex.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "claude-opus-4.7"})
	if err := st.Pipe(context.Background(), bytes.NewReader(in), &bytes.Buffer{}); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	fu := st.FinalUsage()
	if fu.InputTokens == 0 {
		t.Error("FinalUsage.InputTokens should be non-zero")
	}
	if fu.CacheReadInputTokens == 0 {
		t.Error("FinalUsage.CacheReadInputTokens should be non-zero")
	}
}

// TestStreamTranslator_Collect verifies that Collect returns the same SSE
// bytes as Pipe (since Collect is Pipe into a buffer).
func TestStreamTranslator_Collect(t *testing.T) {
	in, err := os.ReadFile("testdata/stream/text-only.codex.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	st1 := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "claude-opus-4.7"})
	var buf bytes.Buffer
	if err := st1.Pipe(context.Background(), bytes.NewReader(in), &buf); err != nil {
		t.Fatalf("Pipe: %v", err)
	}

	st2 := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "claude-opus-4.7"})
	got, err := st2.Collect(context.Background(), bytes.NewReader(in))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if normalize(string(got)) != normalize(buf.String()) {
		t.Errorf("Collect output differs from Pipe:\nCollect:\n%s\nPipe:\n%s", got, buf.String())
	}
}

// TestStreamTranslator_FinalUsage_NoCompleted verifies that FinalUsage
// returns zero values when no response.completed event was seen.
func TestStreamTranslator_FinalUsage_NoCompleted(t *testing.T) {
	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "x", Model: "y"})
	// Run an empty stream — no completed event.
	if err := st.Pipe(context.Background(), strings.NewReader("data: [DONE]\n\n"), &bytes.Buffer{}); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	fu := st.FinalUsage()
	if fu.InputTokens != 0 || fu.OutputTokens != 0 || fu.CacheReadInputTokens != 0 {
		t.Errorf("expected zero FinalUsage, got %+v", fu)
	}
}

// TestStreamTranslator_DropIgnoredEvents verifies that response.in_progress,
// response.content_part.added, response.reasoning_summary_part.added, and
// response.output_item.done are silently dropped.
func TestStreamTranslator_DropIgnoredEvents(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.in_progress"}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1"}}`,
		``,
		`data: {"type":"response.content_part.added"}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		``,
		`data: {"type":"response.output_text.done"}`,
		``,
		`data: {"type":"response.output_item.done"}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	got := out.String()
	// Count only the SSE event header lines (not JSON data lines).
	if strings.Count(got, "event: content_block_start") != 1 {
		t.Errorf("expected 1 content_block_start event, got:\n%s", got)
	}
	if strings.Contains(got, "response.in_progress") {
		t.Errorf("in_progress should be dropped, got:\n%s", got)
	}
}

// TestStreamTranslator_MalformedLineSkipped verifies that a malformed
// JSON data: line is tolerated and does not terminate the stream.
func TestStreamTranslator_MalformedLineSkipped(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: NOT_VALID_JSON`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		``,
		`data: {"type":"response.output_text.done"}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	if !strings.Contains(out.String(), "message_stop") {
		t.Errorf("expected message_stop in output, got:\n%s", out.String())
	}
}

// TestStreamTranslator_DuplicateCreated verifies that a second response.created
// event is silently dropped (messageStarted guard).
func TestStreamTranslator_DuplicateCreated(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	// Should have exactly one message_start event header line.
	// Count occurrences of "event: message_start" (appears once as the header, regardless of newline prefix).
	if strings.Count(out.String(), "event: message_start") != 1 {
		t.Errorf("expected exactly 1 message_start event, got:\n%s", out.String())
	}
}

// TestStreamTranslator_UnknownItemType verifies that an output_item.added with
// an unknown item type is silently dropped and does not increment the block index.
func TestStreamTranslator_UnknownItemType(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"unknown_future_type","id":"x1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"m1"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		``,
		`data: {"type":"response.output_text.done"}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	// The text block should appear at index 0 (unknown type did not consume an index).
	if !strings.Contains(out.String(), `"index":0`) {
		t.Errorf("expected text block at index 0 after unknown item dropped, got:\n%s", out.String())
	}
}

// TestStreamTranslator_NilItemInOutputItemAdded verifies that an
// output_item.added event with no item field is silently dropped.
func TestStreamTranslator_NilItemInOutputItemAdded(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	// No content_block_start should appear.
	if strings.Contains(out.String(), "content_block_start") {
		t.Errorf("unexpected content_block_start when item is nil, got:\n%s", out.String())
	}
}

// TestStreamTranslator_MaxTokensStopReason verifies that a response.completed
// with status="length" maps to stop_reason="max_tokens".
func TestStreamTranslator_MaxTokensStopReason(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"truncated"}`,
		``,
		`data: {"type":"response.output_text.done"}`,
		``,
		`data: {"type":"response.completed","status":"length","usage":{"input_tokens":10,"output_tokens":5}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	if !strings.Contains(out.String(), `"stop_reason":"max_tokens"`) {
		t.Errorf("expected max_tokens stop_reason, got:\n%s", out.String())
	}
}

// TestStreamTranslator_CloseBlockNoop verifies that closeBlock() on a translator
// with no open block is a no-op (does not emit an event).
func TestStreamTranslator_CloseBlockNoop(t *testing.T) {
	// A stream that sends _done events without a matching output_item.added.
	// This exercises the currentBlockIdx < 0 guard in closeBlock.
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.output_text.done"}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	var out bytes.Buffer
	if err := st.Pipe(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	// Should not emit content_block_stop since no block was open.
	if strings.Contains(out.String(), "content_block_stop") {
		t.Errorf("unexpected content_block_stop, got:\n%s", out.String())
	}
}

// TestStreamTranslator_WriteSSEFlush verifies that Pipe flushes the writer if it
// implements the Flush() interface (exercising the Flush branch in writeSSE).
func TestStreamTranslator_WriteSSEFlush(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: {"type":"response.completed","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	fw := &flushWriter{buf: &bytes.Buffer{}}
	if err := st.Pipe(context.Background(), strings.NewReader(input), fw); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	if fw.flushCount == 0 {
		t.Error("expected Flush to be called at least once")
	}
}

// flushWriter wraps bytes.Buffer and counts calls to Flush.
type flushWriter struct {
	buf        *bytes.Buffer
	flushCount int
}

func (f *flushWriter) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *flushWriter) Flush()                       { f.flushCount++ }

// TestStreamTranslator_WriteError verifies that a write error from the dst
// writer is propagated back from Pipe.
func TestStreamTranslator_WriteError(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1"}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "msg_test", Model: "m"})
	if err := st.Pipe(context.Background(), strings.NewReader(input), &errorWriter{}); err == nil {
		t.Error("expected write error to propagate, got nil")
	}
}

// errorWriter always returns an error on Write.
type errorWriter struct{}

func (e *errorWriter) Write(_ []byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// TestStreamTranslator_Collect_ScanError verifies that Collect propagates a
// scanner read error returned by the source reader.
func TestStreamTranslator_Collect_ScanError(t *testing.T) {
	st := translator.NewStreamTranslator(translator.StreamOpts{MessageID: "x", Model: "y"})
	_, err := st.Collect(context.Background(), &errorReader{})
	if err == nil {
		t.Error("expected read error from Collect, got nil")
	}
}

// errorReader returns an error on the first Read call.
type errorReader struct{}

func (e *errorReader) Read(_ []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

// normalize strips trailing whitespace per line and tolerates \r\n vs \n.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}
