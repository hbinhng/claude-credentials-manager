//go:build !windows

package cmd

import (
	"fmt"
	"io"
	"net"
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

// strPtr returns &s — small helper for atomic.Pointer[string].
func strPtr(s string) *string { return &s }

// TestAcceptanceLaunchLoadBalanceRotation drives `ccm launch
// --load-balance` end-to-end via runLaunchLoadBalance, with the
// subprocess exec stubbed to synthesize HTTP traffic through the
// LocalProxy. We assert that:
//
//  1. The first request reaches upstream with the initial activated
//     credential's OAuth bearer.
//  2. After flipping the feasibility profile and waiting for one
//     scheduler tick (synchronized via LastSchedulerTickDoneForTest),
//     the second request reaches upstream with the rotated-to
//     credential's OAuth bearer.
//
// The test uses a 30s rebalance interval (the minimum allowed by
// validateRebalanceDuration) and a 45s deadline on the tick wait.
// It is gated under -short because of that wall-clock cost.
func TestAcceptanceLaunchLoadBalanceRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("acceptance test waits ~30s for a real scheduler tick; skipped under -short")
	}

	// Clear any scheduler stashed by a prior test in this binary
	// so LastSchedulerTickDoneForTest reflects the scheduler this
	// test starts.
	share.ResetLastSchedulerForTest()
	t.Cleanup(share.ResetLastSchedulerForTest)

	setupAcceptanceFakeHome(t)

	now := time.Now()

	// currentProfile names the credential that should have HIGH
	// feasibility on the next probe. We map probe input (token) to
	// the live cred ID via store.List() so token rotation does not
	// break the mapping.
	currentProfile := atomic.Pointer[string]{}
	currentProfile.Store(strPtr("alice"))

	highInfo := &oauth.UsageInfo{Quotas: []oauth.Quota{
		{Name: "5h", Used: 5, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
		{Name: "7d", Used: 1, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
	}}
	lowInfo := &oauth.UsageInfo{Quotas: []oauth.Quota{
		{Name: "5h", Used: 90, ResetsAt: now.Add(4 * time.Hour).Format(time.RFC3339)},
		{Name: "7d", Used: 80, ResetsAt: now.Add(7 * 24 * time.Hour).Format(time.RFC3339)},
	}}

	origUsage := oauth.FetchUsageFn
	oauth.FetchUsageFn = func(token string) *oauth.UsageInfo {
		// Map token → cred name via the live store. Tokens may
		// rotate during refresh; reading the store keeps the mapping
		// fresh.
		creds, _ := store.List()
		for _, c := range creds {
			if c.ClaudeAiOauth.AccessToken == token {
				if c.Name == *currentProfile.Load() {
					return highInfo
				}
				return lowInfo
			}
		}
		return lowInfo
	}
	t.Cleanup(func() { oauth.FetchUsageFn = origUsage })

	// Stub upstream — record bearer per request.
	var bearersMu sync.Mutex
	var bearers []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearersMu.Lock()
		bearers = append(bearers, r.Header.Get("Authorization"))
		bearersMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	share.SetUpstreamBaseForTest(upstream.URL)
	t.Cleanup(share.ResetUpstreamBaseForTest)

	// Stub launchExecFn: synthesize HTTP traffic through the proxy.
	// The seam is invoked with the env that includes
	// ANTHROPIC_BASE_URL pointing at the LocalProxy.
	launchDone := make(chan struct{})
	rotateNow := make(chan struct{})
	firstReqDone := make(chan struct{})
	var execErr atomic.Pointer[error]

	restoreExec := share.SetLaunchExecFnForTest(func(name string, args []string, env []string) error {
		var baseURL string
		// Use the LAST ANTHROPIC_BASE_URL in env so an inherited value
		// from the parent shell does not win over the proxy's address
		// that runLaunchLoadBalance appended last.
		for _, e := range env {
			if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
				baseURL = strings.TrimPrefix(e, "ANTHROPIC_BASE_URL=")
			}
		}
		if baseURL == "" {
			err := fmt.Errorf("no ANTHROPIC_BASE_URL in env")
			execErr.Store(&err)
			close(launchDone)
			return err
		}

		// Wait for the LocalProxy listener to actually be accepting
		// before firing the first request — proxy.Start() runs in a
		// goroutine inside runLaunchLoadBalance, so the kernel socket
		// is bound but the http.Server may not yet be in Serve.
		target := strings.TrimPrefix(baseURL, "http://")
		readyDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(readyDeadline) {
			conn, derr := net.Dial("tcp", target)
			if derr == nil {
				_ = conn.Close()
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		// First request — should reach upstream with alice's bearer.
		req1, _ := http.NewRequest("POST", baseURL+"/v1/messages", strings.NewReader(`{}`))
		resp1, err := http.DefaultClient.Do(req1)
		if err != nil {
			execErr.Store(&err)
			close(firstReqDone)
			close(launchDone)
			return err
		}
		_, _ = io.Copy(io.Discard, resp1.Body)
		resp1.Body.Close()
		close(firstReqDone)

		// Wait for the rotation signal from the test, then request again.
		<-rotateNow
		req2, _ := http.NewRequest("POST", baseURL+"/v1/messages", strings.NewReader(`{}`))
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			execErr.Store(&err)
			close(launchDone)
			return err
		}
		_, _ = io.Copy(io.Discard, resp2.Body)
		resp2.Body.Close()

		close(launchDone)
		return nil
	})
	defer restoreExec()

	// Spawn the launch in a goroutine.
	launchExitC := make(chan error, 1)
	go func() {
		launchExitC <- runLaunchLoadBalance([]string{}, []string{}, 30*time.Second)
	}()

	// Wait for the stub to complete its first HTTP request.
	select {
	case <-firstReqDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("first request did not complete within 10s; bearers=%v", bearers)
	}

	bearersMu.Lock()
	if len(bearers) == 0 || !strings.Contains(bearers[0], "atk-alice") {
		t.Errorf("first request bearer = %v, want atk-alice", bearers)
	}
	bearersMu.Unlock()

	// Drain any tick pulse that may have already fired between
	// StartPoolBackground returning and now (size-1 buffer
	// coalesces, but the buffered pulse would predate the profile
	// flip and falsely satisfy the wait below).
	tickDone := share.LastSchedulerTickDoneForTest()
	if tickDone == nil {
		t.Fatal("LastSchedulerTickDoneForTest returned nil — StartPoolBackground was never called")
	}
	select {
	case <-tickDone:
	default:
	}

	// Flip the profile; the next scheduler tick will rotate.
	currentProfile.Store(strPtr("bob"))

	// Wait deterministically for the post-flip tick to complete.
	select {
	case <-tickDone:
		// rotation completed
	case <-time.After(45 * time.Second):
		t.Fatal("scheduler tick did not complete within 45s")
	}

	// Signal the stubbed claude to make its second request.
	close(rotateNow)
	<-launchDone

	bearersMu.Lock()
	last := bearers[len(bearers)-1]
	bearersMu.Unlock()
	if !strings.Contains(last, "atk-bob") {
		t.Errorf("second request bearer = %q, want atk-bob (rotation should have happened)", last)
	}

	if p := execErr.Load(); p != nil {
		t.Errorf("exec error: %v", *p)
	}

	// Ensure runLaunchLoadBalance returns cleanly.
	select {
	case err := <-launchExitC:
		if err != nil {
			t.Errorf("runLaunchLoadBalance returned err: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runLaunchLoadBalance did not return within 5s of stub exec returning")
	}
}
