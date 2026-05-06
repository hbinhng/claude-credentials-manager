package usage

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sseEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

func TestSSESink_BasicMessageStartAndDelta(t *testing.T) {
	stream := sseEvent("message_start",
		`{"type":"message_start","message":{"model":"claude-opus-4-7-20251217","usage":{"input_tokens":100,"cache_read_input_tokens":50,"cache_creation_input_tokens":25,"output_tokens":1}}}`,
	) + sseEvent("content_block_start", `{"index":0}`) +
		sseEvent("message_delta", `{"type":"message_delta","usage":{"output_tokens":42}}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)

	s := newSSESink()
	if _, err := s.Write([]byte(stream)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rec := s.Finalize()
	if rec == nil {
		t.Fatalf("Finalize returned nil")
	}
	if rec.Model != "claude-opus-4-7-20251217" {
		t.Errorf("Model = %q", rec.Model)
	}
	if rec.In != 100 || rec.CR != 50 || rec.CW != 25 || rec.Out != 42 {
		t.Errorf("counters = in=%d cr=%d cw=%d out=%d, want 100/50/25/42",
			rec.In, rec.CR, rec.CW, rec.Out)
	}
	if !rec.Stream {
		t.Errorf("Stream = false, want true")
	}
}

func TestSSESink_DeltaCarriesInputFields_OverwriteNotAdd(t *testing.T) {
	stream := sseEvent("message_start",
		`{"type":"message_start","message":{"model":"x","usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}}}`,
	) + sseEvent("message_delta",
		`{"type":"message_delta","usage":{"input_tokens":15,"cache_read_input_tokens":7,"output_tokens":99}}`,
	)
	s := newSSESink()
	s.Write([]byte(stream))
	rec := s.Finalize()
	if rec == nil {
		t.Fatalf("nil rec")
	}
	if rec.In != 15 || rec.CR != 7 || rec.Out != 99 {
		t.Errorf("counters = in=%d cr=%d out=%d, want 15/7/99 (overwrite, not add)",
			rec.In, rec.CR, rec.Out)
	}
}

func TestSSESink_LastDeltaWins(t *testing.T) {
	stream := sseEvent("message_start",
		`{"type":"message_start","message":{"model":"x","usage":{"input_tokens":1,"output_tokens":1}}}`,
	) +
		sseEvent("message_delta", `{"usage":{"output_tokens":10}}`) +
		sseEvent("message_delta", `{"usage":{"output_tokens":20}}`) +
		sseEvent("message_delta", `{"usage":{"output_tokens":30}}`)
	s := newSSESink()
	s.Write([]byte(stream))
	rec := s.Finalize()
	if rec.Out != 30 {
		t.Errorf("Out = %d, want 30 (last delta wins)", rec.Out)
	}
}

func TestSSESink_PoisonOnErrorEvent(t *testing.T) {
	stream := sseEvent("message_start",
		`{"type":"message_start","message":{"model":"x","usage":{"input_tokens":50,"output_tokens":1}}}`,
	) + sseEvent("error", `{"type":"error","error":{"type":"overloaded_error"}}`) +
		sseEvent("message_delta", `{"usage":{"output_tokens":99}}`)
	s := newSSESink()
	s.Write([]byte(stream))
	if rec := s.Finalize(); rec != nil {
		t.Fatalf("Finalize after poison should return nil, got %+v", rec)
	}
}

func TestSSESink_AllZerosReturnsNil(t *testing.T) {
	stream := sseEvent("message_start",
		`{"type":"message_start","message":{"model":"x","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
	)
	s := newSSESink()
	s.Write([]byte(stream))
	if rec := s.Finalize(); rec != nil {
		t.Fatalf("all-zero usage should return nil, got %+v", rec)
	}
}

func TestSSESink_LineBufferOverflowResyncs(t *testing.T) {
	junk := bytes.Repeat([]byte("X"), 40*1024)
	good := sseEvent("message_start",
		`{"type":"message_start","message":{"model":"x","usage":{"input_tokens":7,"output_tokens":1}}}`,
	)
	stream := append(junk, '\n')
	stream = append(stream, good...)
	s := newSSESink()
	if _, err := s.Write(stream); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rec := s.Finalize()
	if rec == nil || rec.In != 7 {
		t.Fatalf("after overflow resync: rec=%+v, want In=7", rec)
	}
}

func TestSSESink_MalformedDataLineSkipped(t *testing.T) {
	stream := sseEvent("message_start", `not-json{`) +
		sseEvent("message_start",
			`{"type":"message_start","message":{"model":"x","usage":{"input_tokens":3,"output_tokens":1}}}`,
		)
	s := newSSESink()
	s.Write([]byte(stream))
	rec := s.Finalize()
	if rec == nil || rec.In != 3 {
		t.Fatalf("rec = %+v, want In=3 (malformed line skipped)", rec)
	}
}

func TestSSESink_CRLFTolerated(t *testing.T) {
	// Same as basic but with CRLF line endings.
	stream := "event: message_start\r\n" +
		`data: {"type":"message_start","message":{"model":"x","usage":{"input_tokens":5,"output_tokens":1}}}` + "\r\n" +
		"\r\n"
	s := newSSESink()
	s.Write([]byte(stream))
	rec := s.Finalize()
	if rec == nil || rec.In != 5 {
		t.Fatalf("CRLF rec = %+v, want In=5", rec)
	}
}

func TestSSESink_NonUsageEventIgnored(t *testing.T) {
	// content_block_delta carries a "data:" but no usage; sink stays empty.
	stream := sseEvent("content_block_delta", `{"index":0,"delta":{"text":"hi"}}`)
	s := newSSESink()
	s.Write([]byte(stream))
	if rec := s.Finalize(); rec != nil {
		t.Fatalf("non-usage event yielded record: %+v", rec)
	}
}

func TestJSONSink_Decodes(t *testing.T) {
	body := `{"id":"msg_01","model":"claude-haiku-4-5-20240920","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":0}}`
	s := newJSONSink()
	s.Write([]byte(body))
	rec := s.Finalize()
	if rec == nil {
		t.Fatalf("nil rec")
	}
	if rec.Model != "claude-haiku-4-5-20240920" {
		t.Errorf("Model = %q", rec.Model)
	}
	if rec.In != 10 || rec.Out != 20 || rec.CR != 5 || rec.CW != 0 {
		t.Errorf("counters mismatch: %+v", rec)
	}
	if rec.Stream {
		t.Errorf("Stream = true, want false")
	}
}

func TestJSONSink_AllZerosReturnsNil(t *testing.T) {
	body := `{"model":"x","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`
	s := newJSONSink()
	s.Write([]byte(body))
	if rec := s.Finalize(); rec != nil {
		t.Fatalf("all-zero usage should return nil")
	}
}

func TestJSONSink_MalformedReturnsNil(t *testing.T) {
	s := newJSONSink()
	s.Write([]byte(`not valid json {`))
	if rec := s.Finalize(); rec != nil {
		t.Fatalf("malformed body should return nil")
	}
}

// Forces derefOr's nil-pointer branch: JSON without any usage fields.
func TestJSONSink_MissingUsageFieldsReturnsNil(t *testing.T) {
	s := newJSONSink()
	s.Write([]byte(`{"model":"x"}`))
	if rec := s.Finalize(); rec != nil {
		t.Fatalf("missing usage block should return nil, got %+v", rec)
	}
}

// Mixed presence: only some fields set; missing ones go through nil branch.
func TestJSONSink_PartialUsageFields(t *testing.T) {
	s := newJSONSink()
	s.Write([]byte(`{"model":"x","usage":{"output_tokens":42}}`))
	rec := s.Finalize()
	if rec == nil {
		t.Fatalf("non-zero output should yield record")
	}
	if rec.In != 0 || rec.Out != 42 || rec.CR != 0 || rec.CW != 0 {
		t.Errorf("partial decode = %+v, want In=0 Out=42 CR=0 CW=0", rec)
	}
}

func TestNewSink_DispatchesByContentType(t *testing.T) {
	cases := []struct {
		ct      string
		wantSSE bool
		wantNil bool
	}{
		{"text/event-stream", true, false},
		{"text/event-stream; charset=utf-8", true, false},
		{"application/json", false, false},
		{"application/json; charset=utf-8", false, false},
		{"", false, false}, // empty CT → JSON fallback
		{";;malformed;;", false, true},
	}
	for _, c := range cases {
		t.Run(c.ct, func(t *testing.T) {
			h := http.Header{}
			if c.ct != "" {
				h.Set("Content-Type", c.ct)
			}
			sink := NewSink("sid", h, time.Now().UTC())
			if c.wantNil {
				if sink != nil {
					t.Errorf("got non-nil sink for malformed CT")
				}
				return
			}
			if sink == nil {
				t.Fatalf("got nil sink")
			}
			_, isSSE := sink.(*sseSink)
			if isSSE != c.wantSSE {
				t.Errorf("isSSE = %v, want %v", isSSE, c.wantSSE)
			}
		})
	}
}

func TestTeeBody_FinalizesOnEOF(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	body := strings.NewReader(sseEvent("message_start",
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":11,"output_tokens":1}}}`,
	))
	sink := NewSink(sid, http.Header{"Content-Type": []string{"text/event-stream"}}, time.Now().UTC())
	tee := TeeBody(io.NopCloser(body), sink, sid)
	if _, err := io.Copy(io.Discard, tee); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	tee.Close()
	recs, err := LoadFile(SessionPath(sid))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(recs) != 1 || recs[0].In != 11 {
		t.Fatalf("recs = %+v, want one record with In=11", recs)
	}
}

func TestTeeBody_FinalizeIdempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	body := strings.NewReader(sseEvent("message_start",
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":7,"output_tokens":1}}}`,
	))
	sink := NewSink(sid, http.Header{"Content-Type": []string{"text/event-stream"}}, time.Now().UTC())
	tee := TeeBody(io.NopCloser(body), sink, sid)
	io.Copy(io.Discard, tee)
	tee.Close()
	tee.Close()
	recs, _ := LoadFile(SessionPath(sid))
	if len(recs) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(recs))
	}
}

type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, errors.New("upstream reset") }
func (errReader) Close() error               { return nil }

func TestTeeBody_FinalizesOnReadError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	sink := NewSink(sid, http.Header{"Content-Type": []string{"text/event-stream"}}, time.Now().UTC())
	sse := sink.(*sseSink)
	sse.Write([]byte(sseEvent("message_start",
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":99,"output_tokens":1}}}`,
	)))
	tee := TeeBody(errReader{}, sink, sid)
	buf := make([]byte, 16)
	if _, err := tee.Read(buf); err == nil {
		t.Fatalf("expected error from Read")
	}
	tee.Close()
	recs, _ := LoadFile(SessionPath(sid))
	if len(recs) != 1 || recs[0].In != 99 {
		t.Fatalf("expected partial record with In=99, got %+v", recs)
	}
}

type panicSink struct{}

func (panicSink) Write(p []byte) (int, error) { panic("test panic") }
func (panicSink) Finalize() *Record           { return nil }

func TestTeeBody_RecoversFromSinkPanic(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	body := strings.NewReader("anything")
	tee := TeeBody(io.NopCloser(body), panicSink{}, "5f2c8c4e-1234-4567-8abc-0123456789ab")
	if _, err := io.Copy(io.Discard, tee); err != nil {
		t.Fatalf("Copy errored: %v (panic should have been recovered)", err)
	}
	tee.Close()
}

type panicFinalizeSink struct{}

func (panicFinalizeSink) Write(p []byte) (int, error) { return len(p), nil }
func (panicFinalizeSink) Finalize() *Record           { panic("finalize panic") }

func TestTeeBody_RecoversFromFinalizePanic(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	body := strings.NewReader("anything")
	tee := TeeBody(io.NopCloser(body), panicFinalizeSink{}, "5f2c8c4e-1234-4567-8abc-0123456789ab")
	io.Copy(io.Discard, tee)
	// The Close call triggers finalize which panics; recover() must
	// swallow it and return cleanly.
	if err := tee.Close(); err != nil {
		t.Fatalf("Close errored: %v", err)
	}
}

func TestTeeBody_SkipsInvalidSessionID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	body := strings.NewReader(sseEvent("message_start",
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`,
	))
	sink := NewSink("not-a-uuid", http.Header{"Content-Type": []string{"text/event-stream"}}, time.Now().UTC())
	tee := TeeBody(io.NopCloser(body), sink, "not-a-uuid")
	io.Copy(io.Discard, tee)
	tee.Close()
	if _, err := LoadFile(SessionPath("not-a-uuid")); err == nil {
		t.Fatalf("LoadFile should have failed (file not created)")
	}
}

func TestTeeBody_NilSinkSafe(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	body := strings.NewReader("ignored")
	tee := TeeBody(io.NopCloser(body), nil, "5f2c8c4e-1234-4567-8abc-0123456789ab")
	io.Copy(io.Discard, tee)
	tee.Close()
}

// Forces the Append-failure debugLog branch in finalize: HOME points
// at a non-buildable usage dir.
func TestTeeBody_AppendFailureLogsAndContinues(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CCM_DEBUG_USAGE", "1")
	// Make ~/.ccm a regular file so EnsureDir inside Append fails.
	if err := os.WriteFile(filepath.Join(tmp, ".ccm"), []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(sseEvent("message_start",
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":1,"output_tokens":1}}}`,
	))
	sink := NewSink("5f2c8c4e-1234-4567-8abc-0123456789ab",
		http.Header{"Content-Type": []string{"text/event-stream"}}, time.Now().UTC())
	tee := TeeBody(io.NopCloser(body), sink, "5f2c8c4e-1234-4567-8abc-0123456789ab")
	io.Copy(io.Discard, tee)
	tee.Close() // must not panic
}

func TestTeeBody_ConcurrentReadAndCloseSafe(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sid := "5f2c8c4e-1234-4567-8abc-0123456789ab"
	body := strings.NewReader(sseEvent("message_start",
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":2,"output_tokens":1}}}`,
	))
	sink := NewSink(sid, http.Header{"Content-Type": []string{"text/event-stream"}}, time.Now().UTC())
	tee := TeeBody(io.NopCloser(body), sink, sid)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(io.Discard, tee) }()
	go func() { defer wg.Done(); time.Sleep(time.Millisecond); tee.Close() }()
	wg.Wait()
}

func TestDebugLog_HonorsEnv(t *testing.T) {
	// Just exercise both paths for coverage. Output side-effect is
	// stderr; we don't assert on it here.
	t.Setenv("CCM_DEBUG_USAGE", "")
	debugLog("silent: %d", 1)
	t.Setenv("CCM_DEBUG_USAGE", "1")
	debugLog("loud: %d", 2)
}
