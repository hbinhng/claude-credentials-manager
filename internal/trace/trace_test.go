package trace

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr to a temp buffer for the duration
// of fn, returning the bytes written to it. Tests cannot run in
// parallel because they mutate the global os.Stderr.
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

func TestEnabled_FlagValues(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"yes", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
	}
	for _, c := range cases {
		t.Run(c.v, func(t *testing.T) {
			t.Setenv(EnvVar, c.v)
			if got := Enabled(); got != c.want {
				t.Errorf("Enabled()=%v, want %v", got, c.want)
			}
		})
	}
}

func TestEmit_NoOpWhenDisabled(t *testing.T) {
	t.Setenv(EnvVar, "0")
	out := captureStderr(t, func() {
		Emit("r-x", "in.raw", map[string]any{"body": "hello"})
	})
	if len(out) != 0 {
		t.Errorf("expected no output when disabled, got %q", out)
	}
}

func TestEmit_WritesJSONLine(t *testing.T) {
	t.Setenv(EnvVar, "1")
	out := captureStderr(t, func() {
		Emit("r-1", "in.raw", map[string]any{"body": "hello"})
	})
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Fatalf("expected trailing newline; got %q", out)
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%q", err, out)
	}
	if got["reqId"] != "r-1" || got["dir"] != "in.raw" || got["body"] != "hello" {
		t.Errorf("missing fields: %v", got)
	}
	if got["ts"] == nil {
		t.Errorf("ts not populated: %v", got)
	}
}

func TestEmitRequest_RedactsAuthAndCookie(t *testing.T) {
	t.Setenv(EnvVar, "1")
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer secrettoken")
	h.Set("Cookie", "sid=abc")
	h.Set("Proxy-Authorization", "secret")
	h.Set("X-Custom", "ok")

	out := captureStderr(t, func() {
		EmitRequest("r-2", "upstream.req", "https://example/x", h, []byte(`{"k":"v"}`))
	})
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%q", err, out)
	}
	headers, ok := got["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers missing or wrong type: %v", got)
	}
	if headers["Authorization"] != "[REDACTED]" {
		t.Errorf("Authorization not redacted: %v", headers)
	}
	if headers["Cookie"] != "[REDACTED]" {
		t.Errorf("Cookie not redacted: %v", headers)
	}
	if headers["Proxy-Authorization"] != "[REDACTED]" {
		t.Errorf("Proxy-Authorization not redacted: %v", headers)
	}
	if headers["X-Custom"] != "ok" {
		t.Errorf("X-Custom should pass through: %v", headers)
	}
	if got["url"] != "https://example/x" {
		t.Errorf("url missing: %v", got)
	}
	if got["body"] != `{"k":"v"}` {
		t.Errorf("body missing: %v", got)
	}
}

func TestEmitRequest_NoOpWhenDisabled(t *testing.T) {
	t.Setenv(EnvVar, "0")
	out := captureStderr(t, func() {
		EmitRequest("r", "upstream.req", "https://x", http.Header{"A": []string{"b"}}, []byte("body"))
	})
	if len(out) != 0 {
		t.Errorf("expected no output: %q", out)
	}
}

func TestEmitEvent_FormatsSSE(t *testing.T) {
	t.Setenv(EnvVar, "1")
	out := captureStderr(t, func() {
		EmitEvent("r-3", "out.event", "content_block_delta", `{"index":0}`)
	})
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%q", err, out)
	}
	if got["event"] != "content_block_delta" {
		t.Errorf("event missing: %v", got)
	}
	if got["data"] != `{"index":0}` {
		t.Errorf("data missing: %v", got)
	}
}

func TestEmitEvent_NoOpWhenDisabled(t *testing.T) {
	t.Setenv(EnvVar, "0")
	out := captureStderr(t, func() {
		EmitEvent("r", "out.event", "x", "y")
	})
	if len(out) != 0 {
		t.Errorf("expected no output: %q", out)
	}
}

func TestEmitEvent_OmitsEventKeyWhenEmpty(t *testing.T) {
	t.Setenv(EnvVar, "1")
	out := captureStderr(t, func() {
		EmitEvent("r-4", "out.event", "", "rawchunk")
	})
	var got map[string]any
	_ = json.Unmarshal(bytes.TrimRight(out, "\n"), &got)
	if _, ok := got["event"]; ok {
		t.Errorf("event key should be omitted when empty")
	}
	if got["data"] != "rawchunk" {
		t.Errorf("data missing: %v", got)
	}
}

func TestEmitRequest_DropsEmptyValueHeaders(t *testing.T) {
	// http.Header values are []string; a key with an empty slice is
	// a valid (but unusual) state we should silently drop.
	t.Setenv(EnvVar, "1")
	h := http.Header{
		"X-Filled": []string{"keep"},
		"X-Empty":  []string{},
	}
	out := captureStderr(t, func() {
		EmitRequest("r-6", "in.raw", "", h, nil)
	})
	var got map[string]any
	_ = json.Unmarshal(bytes.TrimRight(out, "\n"), &got)
	headers := got["headers"].(map[string]any)
	if headers["X-Filled"] != "keep" {
		t.Errorf("X-Filled missing: %v", headers)
	}
	if _, ok := headers["X-Empty"]; ok {
		t.Errorf("X-Empty should be dropped: %v", headers)
	}
}

func TestEmitRequest_OmitsKeysWhenInputsAreNil(t *testing.T) {
	t.Setenv(EnvVar, "1")
	out := captureStderr(t, func() {
		EmitRequest("r-5", "in.raw", "", nil, nil)
	})
	var got map[string]any
	_ = json.Unmarshal(bytes.TrimRight(out, "\n"), &got)
	if _, ok := got["url"]; ok {
		t.Errorf("url should be omitted when empty")
	}
	if _, ok := got["headers"]; ok {
		t.Errorf("headers should be omitted when nil")
	}
	if _, ok := got["body"]; ok {
		t.Errorf("body should be omitted when nil")
	}
}

func TestMintReqID_IsUUIDv7Format(t *testing.T) {
	id := MintReqID()
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Errorf("not a UUID-shaped string: %q", id)
	}
	// UUIDv7 has version nibble 7 in the third group.
	if id[14] != '7' {
		t.Errorf("expected v7 (id[14]='7'), got %q", id[14:15])
	}
}

func TestWithReqIDAndReqIDFrom(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := ReqIDFrom(r); got != "" {
		t.Errorf("expected empty reqId on bare request, got %q", got)
	}
	r2 := WithReqID(r, "abc-123")
	if got := ReqIDFrom(r2); got != "abc-123" {
		t.Errorf("ReqIDFrom = %q, want abc-123", got)
	}
	// Original unchanged.
	if got := ReqIDFrom(r); got != "" {
		t.Errorf("WithReqID mutated original; got %q", got)
	}
}

func TestEmit_ConcurrentWritesDontInterleave(t *testing.T) {
	t.Setenv(EnvVar, "1")
	out := captureStderr(t, func() {
		done := make(chan struct{})
		for i := 0; i < 50; i++ {
			go func(i int) {
				Emit("r", "out.event", map[string]any{"i": i})
				done <- struct{}{}
			}(i)
		}
		for i := 0; i < 50; i++ {
			<-done
		}
	})
	// Every line should parse as valid JSON.
	for _, line := range bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(line, &v); err != nil {
			t.Fatalf("interleaved write produced invalid JSON: %v\nline=%q", err, line)
		}
	}
}
