package share

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
