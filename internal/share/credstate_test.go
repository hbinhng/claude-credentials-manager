package share

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// setupFakeHome sets $HOME to a fresh temp dir and creates ~/.ccm so the
// store package's Dir()/CredPath() resolve there. Returns the home path.
func setupFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ccm"), 0700); err != nil {
		t.Fatalf("mkdir ~/.ccm: %v", err)
	}
	return home
}

// makeCred builds a credential with an access token that expires in one
// hour (well clear of IsExpiringSoon's 5-minute window) and saves it to
// the store. Returns the saved credential.
func makeCred(t *testing.T, id, accessToken string) *store.Credential {
	t.Helper()
	cred := &store.Credential{
		ID:   id,
		Name: "test",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  accessToken,
			RefreshToken: "refresh-" + accessToken,
			ExpiresAt:    time.Now().Add(1 * time.Hour).UnixMilli(),
		},
	}
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	return cred
}

func TestCredStateCheapPath(t *testing.T) {
	setupFakeHome(t)
	cred := makeCred(t, "11111111-1111-1111-1111-111111111111", "access-1")

	s := newCredState(cred)
	got, err := s.Fresh()
	if err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if got != "access-1" {
		t.Fatalf("got token %q, want %q", got, "access-1")
	}
}

func TestCredStatePeerWriteReload(t *testing.T) {
	setupFakeHome(t)
	cred := makeCred(t, "22222222-2222-2222-2222-222222222222", "access-old")

	s := newCredState(cred)
	// Prime mtime by calling Fresh once.
	if _, err := s.Fresh(); err != nil {
		t.Fatalf("Fresh (prime): %v", err)
	}

	// Simulate a peer rotating tokens: write a new credential with a
	// different access token to the same path, then bump the file's
	// mtime so our stat-based detection fires.
	peer := *cred
	peer.ClaudeAiOauth.AccessToken = "access-new"
	if err := store.Save(&peer); err != nil {
		t.Fatalf("peer store.Save: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(store.CredPath(cred.ID), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := s.Fresh()
	if err != nil {
		t.Fatalf("Fresh (after peer): %v", err)
	}
	if got != "access-new" {
		t.Fatalf("got token %q, want %q", got, "access-new")
	}
}

// stubTokenServer returns an httptest.Server that answers /v1/oauth/token
// with a fixed token response and counts how many times it was hit.
func stubTokenServer(t *testing.T, access, refresh string, expiresIn int64) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"expires_in":    expiresIn,
			"scope":         "user:inference user:profile",
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// withTokenURL points oauth.TokenURL at srv.URL for the duration of the
// test.
func withTokenURL(t *testing.T, url string) {
	t.Helper()
	orig := oauth.TokenURL
	oauth.TokenURL = url
	t.Cleanup(func() { oauth.TokenURL = orig })
}

func TestCredStateRefreshOnExpiry(t *testing.T) {
	setupFakeHome(t)
	cred := makeCred(t, "33333333-3333-3333-3333-333333333333", "access-old")
	// Force expired.
	cred.ClaudeAiOauth.ExpiresAt = time.Now().Add(-1 * time.Second).UnixMilli()
	if err := store.Save(cred); err != nil {
		t.Fatalf("save expired: %v", err)
	}

	srv, hits := stubTokenServer(t, "access-new", "refresh-new", 3600)
	withTokenURL(t, srv.URL)

	s := newCredState(cred)
	got, err := s.Fresh()
	if err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if got != "access-new" {
		t.Fatalf("got token %q, want %q", got, "access-new")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("oauth.Refresh called %d times, want 1", n)
	}

	// File on disk should now have the new tokens.
	reloaded, err := store.Load(cred.ID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if reloaded.ClaudeAiOauth.AccessToken != "access-new" {
		t.Fatalf("disk access token %q, want %q", reloaded.ClaudeAiOauth.AccessToken, "access-new")
	}
	if reloaded.ClaudeAiOauth.RefreshToken != "refresh-new" {
		t.Fatalf("disk refresh token %q, want %q", reloaded.ClaudeAiOauth.RefreshToken, "refresh-new")
	}
}
