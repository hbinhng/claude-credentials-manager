package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
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
