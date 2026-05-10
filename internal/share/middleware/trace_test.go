package middleware

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/trace"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and
// returns the bytes written.
func captureStderr(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.Bytes()
	}()

	fn()
	_ = w.Close()
	return <-done
}

// parseJSONLines splits stderr capture into one map per JSONL line.
func parseJSONLines(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestTrace_DisabledIsTransparent(t *testing.T) {
	t.Setenv(trace.EnvVar, "")
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		// Body should be readable (not consumed by middleware).
		b, _ := io.ReadAll(r.Body)
		if string(b) != "hello" {
			t.Errorf("inner handler saw body %q, want %q", b, "hello")
		}
		w.WriteHeader(204)
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader("hello"))
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })

	if !called {
		t.Errorf("inner handler not invoked")
	}
	if len(out) != 0 {
		t.Errorf("disabled trace should not write anything, got %q", out)
	}
}

func TestTrace_EmitsInRawAndPropagatesReqID(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	var seenReqID string
	var seenBody []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenReqID = trace.ReqIDFrom(r)
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	h := NewTrace().Apply(inner)

	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"req":"x"}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })

	if seenReqID == "" {
		t.Errorf("inner handler did not receive reqId in context")
	}
	if string(seenBody) != `{"req":"x"}` {
		t.Errorf("inner saw body %q, want %q", seenBody, `{"req":"x"}`)
	}

	lines := parseJSONLines(t, out)
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 lines (in.raw + out.event), got %d:\n%s", len(lines), out)
	}
	first := lines[0]
	if first["dir"] != "in.raw" {
		t.Errorf("first line dir = %v, want in.raw", first["dir"])
	}
	if first["reqId"] != seenReqID {
		t.Errorf("reqId mismatch: log=%v ctx=%q", first["reqId"], seenReqID)
	}
	if first["body"] != `{"req":"x"}` {
		t.Errorf("body in.raw = %v", first["body"])
	}
	if hdrs, ok := first["headers"].(map[string]any); ok {
		if hdrs["Authorization"] != "[REDACTED]" {
			t.Errorf("Authorization not redacted: %v", hdrs)
		}
	}
}

func TestTrace_SSEResponse_OneLinePerEvent(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: a\ndata: 1\n\n"))
		_, _ = w.Write([]byte("event: b\ndata: 2\n\n"))
		_, _ = w.Write([]byte("event: c\ndata: 3\n\n"))
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })

	lines := parseJSONLines(t, out)
	// Expect: in.raw + 3 out.event lines.
	var events []string
	for _, l := range lines {
		if l["dir"] == "out.event" {
			events = append(events, l["event"].(string))
		}
	}
	if len(events) != 3 || events[0] != "a" || events[1] != "b" || events[2] != "c" {
		t.Errorf("expected three SSE events a/b/c, got %v", events)
	}
}

func TestTrace_NonSSEResponse_BufferedFinalEmit(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true,"`))
		_, _ = w.Write([]byte(`"body":"complete"}`))
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })

	lines := parseJSONLines(t, out)
	var bodyLine map[string]any
	for _, l := range lines {
		if l["dir"] == "out.event" {
			bodyLine = l
		}
	}
	if bodyLine == nil {
		t.Fatalf("no out.event line emitted")
	}
	if bodyLine["data"] != `{"ok":true,""body":"complete"}` {
		t.Errorf("buffered body wrong: %v", bodyLine["data"])
	}
}

func TestTrace_ImplicitWriteHeader_DetectsSSEFromContentType(t *testing.T) {
	// Inner handler skips WriteHeader; first Write should still
	// trigger SSE detection from the Content-Type header.
	t.Setenv(trace.EnvVar, "1")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: x\ndata: y\n\n"))
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })
	lines := parseJSONLines(t, out)
	found := false
	for _, l := range lines {
		if l["dir"] == "out.event" && l["event"] == "x" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SSE event x, got %v", lines)
	}
}

func TestTrace_FlushPropagatesToUnderlyingWriter(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	flushed := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: a\ndata: 1\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), onFlush: func() { flushed = true }}
	captureStderr(t, func() { h.ServeHTTP(w, r) })
	if !flushed {
		t.Errorf("Flush not propagated to underlying writer")
	}
}

func TestTrace_TrailingUnterminatedSSEEventEmitted(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: a\ndata: 1\n\n"))
		// Final event missing the "\n\n" terminator.
		_, _ = w.Write([]byte("event: tail\ndata: leftover"))
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })
	lines := parseJSONLines(t, out)
	// We expect the unterminated chunk emitted as a final line with
	// empty event name and the raw bytes as data.
	var sawTrailing bool
	for _, l := range lines {
		if l["dir"] == "out.event" {
			if data, _ := l["data"].(string); strings.Contains(data, "leftover") {
				sawTrailing = true
			}
		}
	}
	if !sawTrailing {
		t.Errorf("trailing unterminated event not emitted: %v", lines)
	}
}

func TestTrace_GetRequestWithNilBody(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	h := NewTrace().Apply(inner)
	// httptest.NewRequest with nil body still yields a non-nil Body
	// per stdlib semantics — explicitly set it to nil to exercise
	// the guarded path.
	r := httptest.NewRequest("GET", "/", nil)
	r.Body = nil
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })
	lines := parseJSONLines(t, out)
	if len(lines) == 0 || lines[0]["dir"] != "in.raw" {
		t.Errorf("expected in.raw line even with nil body: %v", lines)
	}
}

func TestTrace_GzipSSEResponseIsDecodedBeforeSplit(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")

	// Build a small SSE payload and gzip-encode it.
	var raw bytes.Buffer
	raw.WriteString("event: ping\ndata: {\"hello\":\"world\"}\n\n")
	raw.WriteString("event: pong\ndata: {\"goodbye\":\"world\"}\n\n")
	var gz bytes.Buffer
	gzw := gzip.NewWriter(&gz)
	if _, err := gzw.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	gzipped := gz.Bytes()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		_, _ = w.Write(gzipped)
	})
	h := NewTrace().Apply(inner)

	r := httptest.NewRequest("GET", "/v1/messages", nil)
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })

	// Client sees the original gzipped bytes byte-for-byte.
	if got := w.Body.Bytes(); !bytes.Equal(got, gzipped) {
		t.Errorf("client body mismatch: got %d bytes, want %d", len(got), len(gzipped))
	}

	// Trace captured two clean out.event lines (no 0x1f 0x8b magic).
	lines := parseJSONLines(t, out)
	var events []map[string]any
	for _, l := range lines {
		if l["dir"] == "out.event" {
			events = append(events, l)
		}
	}
	if len(events) != 2 {
		t.Fatalf("want 2 out.event lines, got %d", len(events))
	}
	if got, _ := events[0]["event"].(string); got != "ping" {
		t.Errorf("event 0 name = %q, want ping", got)
	}
	if got, _ := events[0]["data"].(string); got != `{"hello":"world"}` {
		t.Errorf("event 0 data = %q", got)
	}
	if got, _ := events[1]["event"].(string); got != "pong" {
		t.Errorf("event 1 name = %q, want pong", got)
	}
}

func TestTrace_NonGzipSSEUnaffected(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: ping\ndata: hi\n\n"))
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("GET", "/v1/messages", nil)
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })

	lines := parseJSONLines(t, out)
	var events []map[string]any
	for _, l := range lines {
		if l["dir"] == "out.event" {
			events = append(events, l)
		}
	}
	if len(events) != 1 {
		t.Fatalf("want 1 out.event line, got %d", len(events))
	}
	if got, _ := events[0]["event"].(string); got != "ping" {
		t.Errorf("event name = %q, want ping", got)
	}
}

// flushRecorder lets us assert Flush propagation.
type flushRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (f *flushRecorder) Flush() { f.onFlush() }

// errBodyReadCloser yields an error on Read so we can exercise the
// "read failed → bodyBytes nil" branch.
type errBodyReadCloser struct{}

func (errBodyReadCloser) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errBodyReadCloser) Close() error             { return nil }

func TestTrace_BodyReadError_IsTolerated(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	h := NewTrace().Apply(inner)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Body = errBodyReadCloser{}
	w := httptest.NewRecorder()
	out := captureStderr(t, func() { h.ServeHTTP(w, r) })
	lines := parseJSONLines(t, out)
	if len(lines) == 0 || lines[0]["dir"] != "in.raw" {
		t.Errorf("expected in.raw line even on body read error: %v", lines)
	}
	// body should be omitted (nil bytes).
	if _, ok := lines[0]["body"]; ok {
		t.Errorf("body should be omitted on read error: %v", lines[0])
	}
}
