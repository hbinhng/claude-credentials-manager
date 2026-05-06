//go:build !windows

package share

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/hbinhng/claude-credentials-manager/internal/usage"
)

// TestLocalProxy_RecordsUsageInSingleCredMode is the regression
// catching the v1.15.0 bug where ccm launch <id> (the single-cred
// passthrough proxy used by `ccm launch <id>` without --load-balance)
// silently dropped every usage record because LocalProxy.ModifyResponse
// was only installed in pool mode.
func TestLocalProxy_RecordsUsageInSingleCredMode(t *testing.T) {
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

	cred := &store.Credential{
		ID:            "11111111-1111-1111-1111-111111111111",
		ClaudeAiOauth: store.OAuthTokens{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()},
	}
	lp, err := NewLocalProxy(cred)
	if err != nil {
		t.Fatalf("NewLocalProxy: %v", err)
	}
	defer lp.Close()
	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Claude-Code-Session-Id", validSID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

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

// Real-world Anthropic responses are gzip-compressed (Go's transport
// auto-adds Accept-Encoding: gzip and we forward that header). The
// regression that prompted this test: v1.15.0 silently skipped every
// gzipped response, recording nothing in production.
func TestLocalProxy_RecordsGzippedSSE(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"claude-opus-4-7-20251217","usage":{"input_tokens":33,"output_tokens":1}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":77}}` + "\n\n"
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	gw.Write([]byte(stream))
	gw.Close()
	compressedBytes := compressed.Bytes()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		w.Write(compressedBytes)
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	cred := &store.Credential{
		ID:            "11111111-1111-1111-1111-111111111111",
		ClaudeAiOauth: store.OAuthTokens{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()},
	}
	lp, err := NewLocalProxy(cred)
	if err != nil {
		t.Fatalf("NewLocalProxy: %v", err)
	}
	defer lp.Close()
	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Claude-Code-Session-Id", validSID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

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
	if recs[0].In != 33 || recs[0].Out != 77 {
		t.Errorf("counters = in=%d out=%d, want 33/77", recs[0].In, recs[0].Out)
	}
	if !recs[0].Stream {
		t.Errorf("Stream = false, want true (was an SSE response)")
	}
}

// Same as RecordsGzippedSSE but for non-streaming gzipped JSON.
func TestLocalProxy_RecordsGzippedJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	body := `{"model":"claude-haiku-4-5-20240920","usage":{"input_tokens":5,"output_tokens":17}}`
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	gw.Write([]byte(body))
	gw.Close()
	compressedBytes := compressed.Bytes()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		w.Write(compressedBytes)
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	cred := &store.Credential{
		ID:            "11111111-1111-1111-1111-111111111111",
		ClaudeAiOauth: store.OAuthTokens{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()},
	}
	lp, err := NewLocalProxy(cred)
	if err != nil {
		t.Fatalf("NewLocalProxy: %v", err)
	}
	defer lp.Close()
	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Claude-Code-Session-Id", validSID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

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
	if recs[0].In != 5 || recs[0].Out != 17 {
		t.Errorf("counters = in=%d out=%d, want 5/17", recs[0].In, recs[0].Out)
	}
	if recs[0].Stream {
		t.Errorf("Stream = true, want false (was application/json)")
	}
}

// Pool mode preserves the 401 → SignalActivatedFailed path AND adds
// usage capture. Both contracts must hold simultaneously.
func TestLocalProxy_RecordsUsageInPoolMode(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w,
			"event: message_start\n"+
				`data: {"type":"message_start","message":{"model":"x","usage":{"input_tokens":7,"output_tokens":1}}}`+"\n\n"+
				"event: message_delta\n"+
				`data: {"type":"message_delta","usage":{"output_tokens":15}}`+"\n\n",
		)
	}))
	defer upstream.Close()
	prevBase := SetUpstreamBaseForTest(upstream.URL)
	defer func() { upstreamBaseOverride = prevBase }()

	stateA := &fakeRefreshableState{id: "aaaaaaaa", expiresAt: time.Now().Add(time.Hour).UnixMilli()}
	pool := &credPool{
		entries:   map[string]*poolEntry{"aaaaaaaa": {state: stateA, status: statusActivated}},
		activated: "aaaaaaaa",
		singleton: true,
	}
	lp, err := NewLocalProxyWithPool(pool, false)
	if err != nil {
		t.Fatalf("NewLocalProxyWithPool: %v", err)
	}
	defer lp.Close()
	go func() { _ = lp.Start() }()
	waitForListener(t, lp.Addr())

	req, _ := http.NewRequest("POST", lp.Addr()+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Claude-Code-Session-Id", validSID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

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
	if recs[0].In != 7 || recs[0].Out != 15 {
		t.Errorf("counters = in=%d out=%d, want 7/15", recs[0].In, recs[0].Out)
	}
}
