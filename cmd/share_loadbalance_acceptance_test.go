//go:build !windows

package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// setupAcceptanceFakeHome makes a fresh ~/.ccm with two creds.
func setupAcceptanceFakeHome(t *testing.T) (a, b *store.Credential) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	a = &store.Credential{
		ID:   "11111111-1111-1111-1111-111111111111",
		Name: "alice",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "atk-alice",
			RefreshToken: "rtk-alice",
			ExpiresAt:    time.Now().Add(6 * time.Hour).UnixMilli(),
		},
	}
	b = &store.Credential{
		ID:   "22222222-2222-2222-2222-222222222222",
		Name: "bob",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "atk-bob",
			RefreshToken: "rtk-bob",
			ExpiresAt:    time.Now().Add(6 * time.Hour).UnixMilli(),
		},
	}
	if err := store.Save(a); err != nil {
		t.Fatalf("save alice: %v", err)
	}
	if err := store.Save(b); err != nil {
		t.Fatalf("save bob: %v", err)
	}
	return a, b
}

func TestAcceptanceLoadBalanceRotation(t *testing.T) {
	a, b := setupAcceptanceFakeHome(t)

	// Stub usage so alice has higher feasibility initially.
	now := time.Now()
	currentProfile := atomic.Pointer[map[string]*oauth.UsageInfo]{}
	highAlice := map[string]*oauth.UsageInfo{
		a.ClaudeAiOauth.AccessToken: {Quotas: []oauth.Quota{
			{Name: "5h", Used: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 1, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
		}},
		b.ClaudeAiOauth.AccessToken: {Quotas: []oauth.Quota{
			{Name: "5h", Used: 90, ResetsAt: now.Add(4 * time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 80, ResetsAt: now.Add(7 * 24 * time.Hour).Format(time.RFC3339)},
		}},
	}
	highBob := map[string]*oauth.UsageInfo{
		a.ClaudeAiOauth.AccessToken: highAlice[b.ClaudeAiOauth.AccessToken],
		b.ClaudeAiOauth.AccessToken: highAlice[a.ClaudeAiOauth.AccessToken],
	}
	currentProfile.Store(&highAlice)

	origUsage := oauth.FetchUsageFn
	oauth.FetchUsageFn = func(token string) *oauth.UsageInfo {
		return (*currentProfile.Load())[token]
	}
	t.Cleanup(func() { oauth.FetchUsageFn = origUsage })

	// Stub upstream (api.anthropic.com) — record which bearer it
	// sees on each request, plus the captured headers, so we can
	// assert per-cred header replay.
	var bearersMu sync.Mutex
	var bearers []string
	var capturedHeaders []http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearersMu.Lock()
		bearers = append(bearers, r.Header.Get("Authorization"))
		capturedHeaders = append(capturedHeaders, r.Header.Clone())
		bearersMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	share.SetUpstreamBaseForTest(upstream.URL)
	t.Cleanup(func() { share.ResetUpstreamBaseForTest() })

	// Stub captureCredFn — with --load-balance, BuildPool/scheduler
	// drive per-cred capture, so this is the seam that controls
	// what headers reach upstream. Stub captureFn too as a defensive
	// belt — some test paths still touch it via the layering.
	restoreCapCred := share.SetCaptureCredFnForTest(func(cred *store.Credential, _ string) (http.Header, error) {
		h := http.Header{}
		h.Set("User-Agent", "acceptance-test")
		h.Set("X-Test-Cred", cred.ID)
		h.Set("Anthropic-Beta", "oauth-2025-04-20")
		return h, nil
	})
	t.Cleanup(restoreCapCred)
	share.SetCaptureFnForTest(func(p *share.Proxy, _ string) error {
		p.MarkCapturedForTest(http.Header{"User-Agent": []string{"acceptance-test"}})
		return nil
	})
	t.Cleanup(share.ResetCaptureFnForTest)

	share.SetCloudflaredFnForTest(func(_ context.Context, _ string) (*share.Tunnel, string, error) {
		return share.NewTunnelForTest(nil), "https://test.example", nil
	})
	t.Cleanup(share.ResetCloudflaredFnForTest)

	// Use a fake clock so we can deterministically drive scheduler
	// ticks. Plumbed through Options.Clock — no global override.
	fc := share.NewFakeClockForTest(now)

	// Drive shareCmd directly via cobra.Execute would also run capture
	// in real time; instead we call the underlying runShareLoadBalance.
	opts := share.Options{
		BindHost:          "127.0.0.1",
		BindPort:          0,
		CapturePrompt:     "acceptance",
		RebalanceInterval: 30 * time.Second,
		Clock:             fc,
	}
	sessC := make(chan share.Session, 1)
	go func() {
		pool, initialCred, err := share.BuildPool(nil, "", false)
		if err != nil {
			t.Errorf("BuildPool: %v", err)
			return
		}
		opts.Pool = pool
		sess, err := share.StartSession(initialCred, opts)
		if err != nil {
			t.Errorf("StartSession: %v", err)
			return
		}
		sessC <- sess
	}()

	var sess share.Session
	select {
	case sess = <-sessC:
	case <-time.After(5 * time.Second):
		t.Fatal("session never started")
	}
	defer sess.Stop()

	// Decode the ticket to get the access token.
	tk := decodeTicket(t, sess.Ticket())
	tkBearer := "Bearer " + tk.Token

	// First request — should reach upstream with alice's bearer.
	req, _ := http.NewRequest("POST", sess.Reach()+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", tkBearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("first POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first POST status = %d, want 200", resp.StatusCode)
	}
	bearersMu.Lock()
	if len(bearers) == 0 || !strings.Contains(bearers[0], "atk-alice") {
		t.Errorf("first request bearer = %v, want atk-alice", bearers)
	}
	if len(capturedHeaders) == 0 {
		t.Errorf("no headers captured for first request")
	} else if got := capturedHeaders[0].Get("X-Test-Cred"); got != a.ID {
		t.Errorf("first request X-Test-Cred = %q, want %q", got, a.ID)
	}
	bearersMu.Unlock()

	// Flip the profile and advance the fake clock past one tick.
	currentProfile.Store(&highBob)
	fc.Advance(31 * time.Second)
	// Allow scheduler goroutine to run.
	time.Sleep(100 * time.Millisecond)

	// Second request — should now use bob's bearer.
	req2, _ := http.NewRequest("POST", sess.Reach()+"/v1/messages", strings.NewReader(`{}`))
	req2.Header.Set("Authorization", tkBearer)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second POST: %v", err)
	}
	resp2.Body.Close()
	bearersMu.Lock()
	last := bearers[len(bearers)-1]
	lastHeaders := capturedHeaders[len(capturedHeaders)-1]
	bearersMu.Unlock()
	if !strings.Contains(last, "atk-bob") {
		t.Errorf("second request bearer = %q, want atk-bob (rotation should have happened)", last)
	}
	if got := lastHeaders.Get("X-Test-Cred"); got != b.ID {
		t.Errorf("second request X-Test-Cred = %q, want %q (per-cred header replay)", got, b.ID)
	}
}

func decodeTicket(t *testing.T, ticket string) share.Ticket {
	t.Helper()
	tk, err := share.DecodeTicket(ticket)
	if err != nil {
		t.Fatalf("decode ticket: %v", err)
	}
	return tk
}
