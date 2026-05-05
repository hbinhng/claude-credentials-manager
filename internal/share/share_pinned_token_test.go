package share

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// pinnedTokenTestEnv wires up the package-level test seams for a
// pinned-token acceptance test: capture stub, errLog buffer,
// upstream stub. Returns a cleanup that reverses every override.
type pinnedTokenTestEnv struct {
	upstream     *httptest.Server
	errBuf       *bytes.Buffer
	upstreamBase string
}

func newPinnedTokenEnv(t *testing.T, upstream http.HandlerFunc) *pinnedTokenTestEnv {
	t.Helper()
	srv := httptest.NewServer(upstream)
	prevBase := SetUpstreamBaseForTest(srv.URL)

	prevCapture := captureFn
	captureFn = func(p *Proxy, _ string) error {
		p.markCaptured(http.Header{
			"User-Agent":        []string{"pinned-test"},
			"Anthropic-Version": []string{"2023-06-01"},
		})
		return nil
	}

	buf := &bytes.Buffer{}
	var bufMu sync.Mutex
	prevErrLog := errLog
	errLog = func() io.Writer {
		return &lockedWriter{mu: &bufMu, w: buf}
	}

	t.Cleanup(func() {
		errLog = prevErrLog
		captureFn = prevCapture
		upstreamBaseOverride = prevBase
		srv.Close()
	})

	return &pinnedTokenTestEnv{
		upstream:     srv,
		errBuf:       buf,
		upstreamBase: srv.URL,
	}
}

// lockedWriter serializes writes to the captured errLog buffer
// because share.go's debug paths and the new pinned-token log line
// can race if multiple goroutines log at once.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestPinnedToken_E2E(t *testing.T) {
	upstreamHits := 0
	env := newPinnedTokenEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	cred := fakeCred("pinned-e2e-0000-0000-0000-000000000001", "tok-real")
	pinnedTok := "pinned-abc-123_DEF"

	sess, err := StartSession(cred, Options{
		BindHost:          "127.0.0.1",
		PinnedAccessToken: pinnedTok,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	// (a) ticket decodes to the pinned token.
	tk, err := DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	if tk.Token != pinnedTok {
		t.Errorf("ticket.Token=%q, want %q", tk.Token, pinnedTok)
	}

	// (d) startup log line emitted exactly once.
	logs := env.errBuf.String()
	const wantLog = "ccm share: using pinned access token"
	if c := strings.Count(logs, wantLog); c != 1 {
		t.Errorf("startup log %q appeared %d times, want 1\nlogs:\n%s", wantLog, c, logs)
	}
	if strings.Contains(logs, pinnedTok) {
		t.Errorf("token contents leaked into errLog:\n%s", logs)
	}

	// (b) request with the right bearer → 200.
	resp, err := doReq(sess.Reach(), "Bearer "+pinnedTok)
	if err != nil {
		t.Fatalf("authed Do: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200; body=%s", resp.StatusCode, readBody(resp))
	}
	resp.Body.Close()
	if upstreamHits != 1 {
		t.Errorf("upstreamHits=%d, want 1", upstreamHits)
	}

	// (c) request with wrong bearer → 401.
	resp2, err := doReq(sess.Reach(), "Bearer wrong-token")
	if err != nil {
		t.Fatalf("unauth Do: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Errorf("wrong-bearer status=%d, want 401", resp2.StatusCode)
	}
	body := readBody(resp2)
	if !strings.Contains(body, "authentication_error") {
		t.Errorf("wrong-bearer body=%q, want authentication_error", body)
	}
}

func TestPinnedToken_StableAcrossRestart(t *testing.T) {
	env := newPinnedTokenEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	_ = env

	cred := fakeCred("pinned-stable-0000-0000-0000-000000000002", "tok-real")
	pinnedTok := "stable-pin-XYZ_123"

	// First "boot".
	sess1, err := StartSession(cred, Options{
		BindHost:          "127.0.0.1",
		PinnedAccessToken: pinnedTok,
	})
	if err != nil {
		t.Fatalf("StartSession 1: %v", err)
	}
	tk1, err := DecodeTicket(sess1.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket 1: %v", err)
	}
	if err := sess1.Stop(); err != nil {
		t.Fatalf("Stop 1: %v", err)
	}

	// Second "boot" — same pinned token, fresh session.
	sess2, err := StartSession(cred, Options{
		BindHost:          "127.0.0.1",
		PinnedAccessToken: pinnedTok,
	})
	if err != nil {
		t.Fatalf("StartSession 2: %v", err)
	}
	defer sess2.Stop()
	tk2, err := DecodeTicket(sess2.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket 2: %v", err)
	}

	if tk1.Token != pinnedTok {
		t.Errorf("ticket1.Token=%q, want %q", tk1.Token, pinnedTok)
	}
	if tk2.Token != pinnedTok {
		t.Errorf("ticket2.Token=%q, want %q", tk2.Token, pinnedTok)
	}

	// Second session's listener accepts the same bearer.
	resp, err := doReq(sess2.Reach(), "Bearer "+pinnedTok)
	if err != nil {
		t.Fatalf("authed Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}

func TestPinnedToken_BindHostLAN(t *testing.T) {
	var seenAuth string
	env := newPinnedTokenEnv(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	_ = env

	cred := fakeCred("pinned-lan-0000-0000-0000-000000000003", "tok-real")
	pinnedTok := "lan-token_1"

	sess, err := StartSession(cred, Options{
		BindHost:          "127.0.0.1",
		PinnedAccessToken: pinnedTok,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	if sess.Mode() != "lan" {
		t.Errorf("Mode=%q, want lan", sess.Mode())
	}

	tk, err := DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	if tk.Scheme != "http" {
		t.Errorf("scheme=%q, want http", tk.Scheme)
	}
	if tk.Token != pinnedTok {
		t.Errorf("ticket.Token=%q, want %q", tk.Token, pinnedTok)
	}
	if !strings.HasPrefix(tk.Host, "127.0.0.1:") {
		t.Errorf("ticket.Host=%q, want 127.0.0.1:<port>", tk.Host)
	}

	resp, err := doReq(sess.Reach(), "Bearer "+pinnedTok)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	// Director rewrites Authorization to the real OAuth bearer.
	if seenAuth == "Bearer "+pinnedTok {
		t.Errorf("upstream saw the inbound bearer %q, director should have replaced it", seenAuth)
	}
	if !strings.HasPrefix(seenAuth, "Bearer ") {
		t.Errorf("upstream Authorization=%q, want Bearer <real>", seenAuth)
	}
}

func TestPinnedToken_LoadBalance_E2E(t *testing.T) {
	upstreamHits := 0
	env := newPinnedTokenEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits++
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	_ = env

	// Stub usage so BuildPool admits both creds without network.
	prevUsage := oauth.FetchUsageFn
	oauth.FetchUsageFn = func(_ string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 5, ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
		}}
	}
	t.Cleanup(func() { oauth.FetchUsageFn = prevUsage })

	// Stub captureCredFn (used by BuildPool, distinct from captureFn).
	restoreCC := SetCaptureCredFnForTest(func(_ *store.Credential, _ string) (http.Header, error) {
		return http.Header{
			"User-Agent":        []string{"pinned-pool-test"},
			"Anthropic-Version": []string{"2023-06-01"},
		}, nil
	})
	t.Cleanup(restoreCC)

	// Build a pool with two creds in a fresh fake home.
	setupFakeHome(t)
	makeCred(t, "pool-cred-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "tok-1")
	makeCred(t, "pool-cred-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "tok-2")

	pool, initialCred, err := BuildPool(nil, "ignored", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if pool == nil || initialCred == nil {
		t.Fatalf("nil pool/initial: %v / %v", pool, initialCred)
	}

	pinnedTok := "pool-bearer_ABC123"

	sess, err := StartSession(initialCred, Options{
		BindHost:          "127.0.0.1",
		PinnedAccessToken: pinnedTok,
		Pool:              pool,
		RebalanceInterval: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	// Confirm the ticket carries the pinned token.
	tk, err := DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	if tk.Token != pinnedTok {
		t.Errorf("ticket.Token=%q, want %q", tk.Token, pinnedTok)
	}

	// 4 requests with the pinned bearer → all 200.
	for i := 0; i < 4; i++ {
		resp, err := doReq(sess.Reach(), "Bearer "+pinnedTok)
		if err != nil {
			t.Fatalf("authed Do %d: %v", i, err)
		}
		body := readBody(resp)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("authed status %d=%d, want 200; body=%s", i, resp.StatusCode, body)
		}
	}
	if upstreamHits != 4 {
		t.Errorf("upstreamHits=%d, want 4", upstreamHits)
	}

	// 1 request with wrong bearer → 401.
	resp, err := doReq(sess.Reach(), "Bearer wrong")
	if err != nil {
		t.Fatalf("unauth Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("wrong-bearer status=%d, want 401", resp.StatusCode)
	}
}

func TestPinnedToken_InvalidCharset(t *testing.T) {
	// No proxy / capture / cloudflared / refresh-timer should run when
	// validation fails; assert via runtime.NumGoroutine delta.
	before := runtime.NumGoroutine()

	cred := fakeCred("pinned-bad-0000-0000-0000-000000000099", "tok")
	sess, err := StartSession(cred, Options{PinnedAccessToken: "hello world"})
	if err == nil {
		if sess != nil {
			_ = sess.Stop()
		}
		t.Fatalf("StartSession succeeded with invalid pinned token")
	}
	if !errors.Is(err, ErrInvalidPinnedToken) {
		t.Errorf("err=%v, want errors.Is ErrInvalidPinnedToken", err)
	}

	// Give the runtime a beat to settle, then check no goroutine
	// leak. The threshold is generous (5) because the test runtime
	// itself can spawn / retire transient goroutines.
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Errorf("goroutine count grew %d → %d on validation failure (>5 delta)", before, after)
	}
}

// --- test helpers ----------------------------------------------------

func doReq(reach, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), "POST", reach+"/v1/messages", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	return http.DefaultClient.Do(req)
}

func readBody(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

