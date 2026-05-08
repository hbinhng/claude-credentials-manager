// Internal white-box tests for branches that require accessing package-level
// variables (collectMaxBytes) or helpers not exported to external test code.
package translator

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
