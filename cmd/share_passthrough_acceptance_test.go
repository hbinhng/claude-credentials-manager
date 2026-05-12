package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// TestSharePassthroughSoloEndToEnd verifies that a solo --passthrough
// invocation: (1) the bootstrap probe succeeds against the upstream's
// /ccm-share/usage, (2) the pool admits exactly one passthrough entry,
// (3) StartSession succeeds with nil cred + InitialEntryID, (4) the
// consumer mints its own access token (different from the upstream's
// ticket bearer).
func TestSharePassthroughSoloEndToEnd(t *testing.T) {
	setupFakeHome(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ccm-share/usage":
			if r.Header.Get("Authorization") != "Bearer upstream-tk" {
				w.WriteHeader(401)
				return
			}
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"v":                   1,
				"feasibility_seconds": 1800.0,
				"activated":           true,
				"degraded":            false,
				"unconstrained":       false,
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")

	share.SetCloudflaredFnForTest(func(ctx context.Context, localURL string) (*share.Tunnel, string, error) {
		return share.NewTunnelForTest(nil), "https://fake.trycloudflare.com", nil
	})
	defer share.ResetCloudflaredFnForTest()

	tk := share.Ticket{Scheme: "http", Host: host, Token: "upstream-tk"}
	seed, err := share.BootstrapPassthroughProbe(tk)
	if err != nil {
		t.Fatalf("BootstrapPassthroughProbe: %v", err)
	}

	pool, initialCred, entry, err := share.BuildPoolFromMixed(nil, []share.PassthroughSeed{seed}, "", false)
	if err != nil {
		t.Fatalf("BuildPoolFromMixed: %v", err)
	}
	if initialCred != nil {
		t.Errorf("solo passthrough pool: initialCred should be nil; got %v", initialCred)
	}
	if entry == nil {
		t.Fatalf("solo passthrough pool: initial entry should be non-nil")
	}
	if !strings.HasPrefix(entry.State().CredID(), "pt:") {
		t.Errorf("entry credID should be a passthrough synth ID; got %q", entry.State().CredID())
	}

	sess, err := share.StartSession(nil, share.Options{
		Pool:              pool,
		InitialEntryID:    entry.State().CredID(),
		InitialEntryName:  entry.State().CredName(),
		RebalanceInterval: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	if sess.CredID() != entry.State().CredID() {
		t.Errorf("session credID = %q, want %q", sess.CredID(), entry.State().CredID())
	}

	consumerTicket, err := share.DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	if consumerTicket.Token == "" {
		t.Errorf("consumer ticket missing token")
	}
	if consumerTicket.Token == "upstream-tk" {
		t.Errorf("consumer ticket bearer must be different from upstream's; both = %q", consumerTicket.Token)
	}
}

// TestSharePassthroughTwoUpstreamsInitialActivated verifies that a
// two-passthrough pool (both healthy) selects the higher-feasibility
// upstream as the initial activated entry.
func TestSharePassthroughTwoUpstreamsInitialActivated(t *testing.T) {
	setupFakeHome(t)

	mkServer := func(feas float64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/ccm-share/usage" {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"v":                   1,
				"feasibility_seconds": feas,
				"activated":           true,
				"degraded":            false,
				"unconstrained":       false,
			})
		}))
	}

	srvA := mkServer(600.0)
	defer srvA.Close()
	srvB := mkServer(3600.0)
	defer srvB.Close()

	hostA := strings.TrimPrefix(srvA.URL, "http://")
	hostB := strings.TrimPrefix(srvB.URL, "http://")

	seedA, err := share.BootstrapPassthroughProbe(share.Ticket{Scheme: "http", Host: hostA, Token: "a"})
	if err != nil {
		t.Fatalf("seed A: %v", err)
	}
	seedB, err := share.BootstrapPassthroughProbe(share.Ticket{Scheme: "http", Host: hostB, Token: "b"})
	if err != nil {
		t.Fatalf("seed B: %v", err)
	}

	pool, initialCred, entry, err := share.BuildPoolFromMixed(nil, []share.PassthroughSeed{seedA, seedB}, "", false)
	if err != nil {
		t.Fatalf("BuildPoolFromMixed: %v", err)
	}
	if initialCred != nil {
		t.Errorf("passthrough-only: initialCred should be nil; got %v", initialCred)
	}
	if len(pool.SnapshotLines()) != 2 {
		t.Errorf("pool size = %d, want 2", len(pool.SnapshotLines()))
	}
	// B has higher feasibility (3600 > 600), so it must be the initial activated.
	if !strings.Contains(entry.State().CredName(), hostB) {
		t.Errorf("initial activated should be B (host %s); got credName=%s", hostB, entry.State().CredName())
	}
}

// TestSharePassthroughMixedPool verifies a pool with one local cred +
// one passthrough seed admits both, captures from the local cred (the
// only one that needs identity headers), and selects the higher-
// feasibility entry as the initial activated.
func TestSharePassthroughMixedPool(t *testing.T) {
	setupFakeHome(t)

	// Seed a local cred in the fake store.
	localCred := &store.Credential{
		ID:   "11111111-1111-1111-1111-111111111111",
		Name: "local-test",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "at",
			RefreshToken: "rt",
			ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		},
		CreatedAt:       "2026-05-12T00:00:00Z",
		LastRefreshedAt: "2026-05-12T00:00:00Z",
	}
	if err := store.Save(localCred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	// Stub oauth.FetchUsageFn so Pass B (probe local cred usage) doesn't
	// hit Anthropic. Return moderate feasibility (well below the
	// passthrough's 7200s).
	origFetch := oauth.FetchUsageFn
	oauth.FetchUsageFn = func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 50.0, ResetsAt: time.Now().Add(2 * time.Hour).Format(time.RFC3339)},
		}}
	}
	defer func() { oauth.FetchUsageFn = origFetch }()

	// Stub captureCredFn so we don't need claude on PATH.
	defer share.SetCaptureCredFnForTest(func(c *store.Credential, prompt string) (http.Header, error) {
		return http.Header{"X-Test-Captured": {"yes"}}, nil
	})()

	// Fake upstream serving /ccm-share/usage with high feasibility.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ccm-share/usage" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"v":                   1,
			"feasibility_seconds": 7200.0,
			"activated":           true,
			"degraded":            false,
			"unconstrained":       false,
		})
	}))
	defer upstream.Close()
	host := strings.TrimPrefix(upstream.URL, "http://")

	seed, err := share.BootstrapPassthroughProbe(share.Ticket{Scheme: "http", Host: host, Token: "tk"})
	if err != nil {
		t.Fatalf("bootstrap probe: %v", err)
	}

	pool, initialCred, entry, err := share.BuildPoolFromMixed([]string{localCred.ID}, []share.PassthroughSeed{seed}, "prompt", false)
	if err != nil {
		t.Fatalf("BuildPoolFromMixed: %v", err)
	}
	if len(pool.SnapshotLines()) != 2 {
		t.Errorf("pool size = %d, want 2 (local + passthrough)", len(pool.SnapshotLines()))
	}
	// Passthrough has feasibility 7200; local cred's feasibility ≈ 2h headroom = much smaller.
	// Passthrough wins → initial activated should have "pt:" prefix.
	if !strings.HasPrefix(entry.State().CredID(), "pt:") {
		t.Errorf("initial activated should be the passthrough; got credID=%s", entry.State().CredID())
	}
	if initialCred != nil {
		t.Errorf("initialCred should be nil when passthrough wins; got %v", initialCred.Name)
	}
}
