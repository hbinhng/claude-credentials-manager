package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestWrapTransport_DisabledReturnsOriginal(t *testing.T) {
	t.Setenv(EnvVar, "")
	rt := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	got := WrapTransport(rt)
	if got == nil {
		t.Fatal("got nil")
	}
	// Same identity, not wrapped.
	gotF, ok := got.(roundTripperFunc)
	if !ok {
		t.Fatalf("expected unwrapped, got %T", got)
	}
	_ = gotF
}

func TestWrapTransport_EnabledTeesRequestAndResponse(t *testing.T) {
	t.Setenv(EnvVar, "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: a\ndata: 1\n\n"))
		_, _ = w.Write([]byte("event: b\ndata: 2\n\n"))
	}))
	t.Cleanup(upstream.Close)

	rt := WrapTransport(http.DefaultTransport)
	req, _ := http.NewRequest("POST", upstream.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req = req.WithContext(context.WithValue(req.Context(), reqIDContextKey{}, "r-T"))

	out := captureStderr(t, func() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		// Drain so the tee fires and the trailing flush runs.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
	lines := parseJSONLinesT(t, out)

	var sawReq bool
	var events []string
	for _, l := range lines {
		switch l["dir"] {
		case "upstream.req":
			sawReq = true
			if l["body"] != `{"model":"gpt-5"}` {
				t.Errorf("upstream.req body wrong: %v", l["body"])
			}
			if hdrs, ok := l["headers"].(map[string]any); ok {
				if hdrs["Authorization"] != "[REDACTED]" {
					t.Errorf("Authorization not redacted: %v", hdrs)
				}
			}
		case "upstream.resp.event":
			events = append(events, l["event"].(string))
		}
		if l["reqId"] != "r-T" {
			t.Errorf("reqId not propagated on %v: %v", l["dir"], l["reqId"])
		}
	}
	if !sawReq {
		t.Errorf("upstream.req not emitted")
	}
	if len(events) != 2 || events[0] != "a" || events[1] != "b" {
		t.Errorf("upstream events wrong: %v", events)
	}
}

func TestWrapTransport_NonSSEResponseBufferedFinalEmit(t *testing.T) {
	t.Setenv(EnvVar, "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"k":"v"}`))
	}))
	t.Cleanup(upstream.Close)

	rt := WrapTransport(nil) // exercise nil → DefaultTransport branch
	req, _ := http.NewRequest("GET", upstream.URL+"/x", nil)

	out := captureStderr(t, func() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
	lines := parseJSONLinesT(t, out)
	var bodyLine map[string]any
	for _, l := range lines {
		if l["dir"] == "upstream.resp.event" {
			bodyLine = l
		}
	}
	if bodyLine == nil {
		t.Fatalf("upstream.resp.event not emitted")
	}
	if bodyLine["data"] != `{"k":"v"}` {
		t.Errorf("non-SSE body wrong: %v", bodyLine["data"])
	}
}

func TestWrapTransport_RoundTripError(t *testing.T) {
	t.Setenv(EnvVar, "1")
	wantErr := errors.New("boom")
	rt := WrapTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, wantErr
	}))
	req, _ := http.NewRequest("POST", "http://example/x", strings.NewReader("body"))
	out := captureStderr(t, func() {
		_, err := rt.RoundTrip(req)
		if !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
	})
	lines := parseJSONLinesT(t, out)
	var errLine map[string]any
	for _, l := range lines {
		if l["dir"] == "upstream.resp.error" {
			errLine = l
		}
	}
	if errLine == nil {
		t.Fatalf("upstream.resp.error not emitted")
	}
	if errLine["body"] != "boom" {
		t.Errorf("error body wrong: %v", errLine["body"])
	}
}

func TestWrapTransport_SSETrailingUnterminatedEventEmitted(t *testing.T) {
	t.Setenv(EnvVar, "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		// First event terminated; second event is truncated mid-stream.
		_, _ = w.Write([]byte("event: ok\ndata: 1\n\n"))
		_, _ = w.Write([]byte("event: trunc\ndata: never-ended"))
	}))
	t.Cleanup(upstream.Close)

	rt := WrapTransport(http.DefaultTransport)
	req, _ := http.NewRequest("GET", upstream.URL+"/x", nil)
	out := captureStderr(t, func() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
	lines := parseJSONLinesT(t, out)
	var sawTrailing bool
	for _, l := range lines {
		if l["dir"] == "upstream.resp.event" {
			data, _ := l["data"].(string)
			if strings.Contains(data, "never-ended") {
				sawTrailing = true
			}
		}
	}
	if !sawTrailing {
		t.Errorf("trailing unterminated upstream event not emitted: %v", lines)
	}
}

// doerFunc adapts a function to the Doer interface.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestWrapDoer_DisabledReturnsOriginal(t *testing.T) {
	t.Setenv(EnvVar, "")
	d := doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	got := WrapDoer(d)
	// Same identity, not wrapped.
	if _, ok := got.(*traceDoer); ok {
		t.Errorf("WrapDoer should be passthrough when disabled")
	}
}

func TestWrapDoer_EnabledTeesRoundTrip(t *testing.T) {
	t.Setenv(EnvVar, "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: ping\ndata: pong\n\n"))
	}))
	t.Cleanup(upstream.Close)

	d := doerFunc(func(r *http.Request) (*http.Response, error) {
		return http.DefaultTransport.RoundTrip(r)
	})
	wrapped := WrapDoer(d)

	req, _ := http.NewRequest("POST", upstream.URL+"/x", strings.NewReader(`{"a":1}`))
	req = req.WithContext(context.WithValue(req.Context(), reqIDContextKey{}, "r-D"))
	out := captureStderr(t, func() {
		resp, err := wrapped.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
	lines := parseJSONLinesT(t, out)
	var sawReq bool
	var sawEvent bool
	for _, l := range lines {
		switch l["dir"] {
		case "upstream.req":
			sawReq = true
			if l["body"] != `{"a":1}` {
				t.Errorf("upstream.req body wrong: %v", l["body"])
			}
		case "upstream.resp.event":
			if l["event"] == "ping" {
				sawEvent = true
			}
		}
	}
	if !sawReq {
		t.Errorf("upstream.req not emitted via WrapDoer")
	}
	if !sawEvent {
		t.Errorf("upstream SSE event not emitted via WrapDoer")
	}
}

func TestWrapTransport_BodyReadError(t *testing.T) {
	t.Setenv(EnvVar, "1")
	called := false
	rt := WrapTransport(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		// Body should still be readable (we restore it from the tee).
		got, _ := io.ReadAll(r.Body)
		// Body read failed at the tee level; we should have an empty
		// body reader instead of forwarding garbage.
		if len(got) != 0 {
			t.Errorf("expected empty body after read error, got %q", got)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}))
	req, _ := http.NewRequest("POST", "http://example/x", errReader{})
	captureStderr(t, func() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_ = resp.Body.Close()
	})
	if !called {
		t.Errorf("downstream RoundTrip not invoked")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errReader) Close() error             { return nil }

func TestWrapTransport_DetectsSSEByBodySniffWhenContentTypeMissing(t *testing.T) {
	t.Setenv(EnvVar, "1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Intentionally NOT setting Content-Type to text/event-stream.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: alpha\ndata: 1\n\n"))
		_, _ = w.Write([]byte("event: beta\ndata: 2\n\n"))
	}))
	t.Cleanup(upstream.Close)

	rt := WrapTransport(http.DefaultTransport)
	req, _ := http.NewRequest("GET", upstream.URL+"/x", nil)
	out := captureStderr(t, func() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	})
	lines := parseJSONLinesT(t, out)
	var events []string
	for _, l := range lines {
		if l["dir"] == "upstream.resp.event" {
			if name, ok := l["event"].(string); ok && name != "" {
				events = append(events, name)
			}
		}
	}
	if len(events) != 2 || events[0] != "alpha" || events[1] != "beta" {
		t.Errorf("expected alpha+beta events from sniff, got %v", events)
	}
}

// parseJSONLinesT is a local helper (named with T suffix to avoid
// clashing with parseJSONLines in trace_test.go that's in the same
// package).
func parseJSONLinesT(t *testing.T, raw []byte) []map[string]any {
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
