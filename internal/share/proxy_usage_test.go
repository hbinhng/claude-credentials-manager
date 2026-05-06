//go:build !windows

package share

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/usage"
)

const validSID = "5f2c8c4e-1234-4567-8abc-0123456789ab"

func TestProxy_RecordsUsageInSingleCredMode(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w,
			"event: message_start\n"+
				`data: {"type":"message_start","message":{"model":"claude-opus-4-7-20251217","usage":{"input_tokens":11,"output_tokens":1}}}`+"\n\n"+
				"event: message_delta\n"+
				`data: {"type":"message_delta","usage":{"output_tokens":22}}`+"\n\n",
		)
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	tokens := &fakeTokenSource{token: "fake-bearer"}
	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{"User-Agent": []string{"claude-code/test"}})
	go proxy.Start()
	if err := proxy.Transition("fake-bearer", tokens, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer fake-bearer")
	req.Header.Set("X-Claude-Code-Session-Id", validSID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// ModifyResponse runs synchronously inside the reverse-proxy
	// goroutine; the usage file should appear shortly after Body.Close.
	path := filepath.Join(tmp, ".ccm", "usage", validSID+".ndjson")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	recs, err := usage.LoadFile(path)
	if err != nil || len(recs) != 1 {
		t.Fatalf("recs = %v err = %v, want 1 record", recs, err)
	}
	if recs[0].In != 11 || recs[0].Out != 22 {
		t.Errorf("counters = in=%d out=%d, want 11/22", recs[0].In, recs[0].Out)
	}
	if recs[0].Model != "claude-opus-4-7-20251217" {
		t.Errorf("model = %q", recs[0].Model)
	}
}

func TestProxy_SkipsUsageWhenSessionIDMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w,
			"event: message_start\n"+
				`data: {"type":"message_start","message":{"model":"x","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n",
		)
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	tokens := &fakeTokenSource{token: "tok"}
	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{})
	go proxy.Start()
	proxy.Transition("tok", tokens, nil)

	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	dir := filepath.Join(tmp, ".ccm", "usage")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("usage dir should be empty, got %d entries: %v", len(entries), entries)
	}
}

func TestProxy_SkipsUsageWhenContentEncodingSet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		w.Write([]byte("\x1f\x8b\x08\x00"))
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	tokens := &fakeTokenSource{token: "tok"}
	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{})
	go proxy.Start()
	proxy.Transition("tok", tokens, nil)

	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Claude-Code-Session-Id", validSID)
	resp, _ := http.DefaultClient.Do(req)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	path := filepath.Join(tmp, ".ccm", "usage", validSID+".ndjson")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("usage file unexpectedly exists for compressed response (err=%v)", err)
	}
}

func TestProxy_SkipsUsageWhenSessionIDInvalid(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w,
			"event: message_start\n"+
				`data: {"type":"message_start","message":{"model":"x","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n",
		)
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	tokens := &fakeTokenSource{token: "tok"}
	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer proxy.Close()
	proxy.markCaptured(http.Header{})
	go proxy.Start()
	proxy.Transition("tok", tokens, nil)

	req, _ := http.NewRequest("POST", proxy.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Claude-Code-Session-Id", "../etc/passwd")
	resp, _ := http.DefaultClient.Do(req)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	dir := filepath.Join(tmp, ".ccm", "usage")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("dir not empty: %v", entries)
	}
}
