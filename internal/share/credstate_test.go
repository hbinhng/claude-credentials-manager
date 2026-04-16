package share

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
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

// TestCredStateFlockExclusive verifies that Fresh() blocks while a
// peer holds flock(LOCK_EX) on the credential's lock file, and
// completes once the peer releases. flock(2) is per open-file-
// description on both Linux and macOS, so two fds in the same
// process serialize correctly.
func TestCredStateFlockExclusive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock not used on Windows")
	}
	setupFakeHome(t)
	cred := makeCred(t, "44444444-4444-4444-4444-444444444444", "access-old")
	cred.ClaudeAiOauth.ExpiresAt = time.Now().Add(-1 * time.Second).UnixMilli()
	if err := store.Save(cred); err != nil {
		t.Fatalf("save expired: %v", err)
	}

	srv, _ := stubTokenServer(t, "access-new", "refresh-new", 3600)
	withTokenURL(t, srv.URL)

	// Hold an exclusive flock on the lock file from a separate fd.
	lockPath := filepath.Join(store.Dir(), cred.ID+".credentials.json.lock")
	peerFd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("peer open lock: %v", err)
	}
	if err := syscall.Flock(int(peerFd.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("peer flock LOCK_EX: %v", err)
	}

	// Release the peer lock 200ms from now.
	release := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() {
		_ = syscall.Flock(int(peerFd.Fd()), syscall.LOCK_UN)
		_ = peerFd.Close()
		close(release)
	})

	s := newCredState(cred)
	start := time.Now()
	got, err := s.Fresh()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if got != "access-new" {
		t.Fatalf("got token %q, want %q", got, "access-new")
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("Fresh returned in %v — expected to block on flock until peer released", elapsed)
	}
	<-release
}

// TestCredStateDoubleCheckSkipsRedundantRefresh verifies that when a
// peer refreshes while we are blocked on the flock, we observe the
// peer's rotation via the post-lock reload and skip our own OAuth
// call.
func TestCredStateDoubleCheckSkipsRedundantRefresh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock not used on Windows")
	}
	setupFakeHome(t)
	cred := makeCred(t, "55555555-5555-5555-5555-555555555555", "access-old")
	cred.ClaudeAiOauth.ExpiresAt = time.Now().Add(-1 * time.Second).UnixMilli()
	if err := store.Save(cred); err != nil {
		t.Fatalf("save expired: %v", err)
	}

	srv, hits := stubTokenServer(t, "access-should-not-be-used", "refresh-x", 3600)
	withTokenURL(t, srv.URL)

	// Take the lock from a separate fd.
	lockPath := filepath.Join(store.Dir(), cred.ID+".credentials.json.lock")
	peerFd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("peer open lock: %v", err)
	}
	if err := syscall.Flock(int(peerFd.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("peer flock LOCK_EX: %v", err)
	}

	// Simulate the peer finishing its refresh while we hold the lock:
	// write a fresh, non-expiring credential to disk, bump mtime, then
	// release the lock so our Fresh() can acquire it.
	time.AfterFunc(150*time.Millisecond, func() {
		fresh := *cred
		fresh.ClaudeAiOauth.AccessToken = "access-peer-refreshed"
		fresh.ClaudeAiOauth.RefreshToken = "refresh-peer"
		fresh.ClaudeAiOauth.ExpiresAt = time.Now().Add(1 * time.Hour).UnixMilli()
		_ = store.Save(&fresh)
		future := time.Now().Add(1 * time.Second)
		_ = os.Chtimes(store.CredPath(cred.ID), future, future)
		_ = syscall.Flock(int(peerFd.Fd()), syscall.LOCK_UN)
		_ = peerFd.Close()
	})

	s := newCredState(cred)
	got, err := s.Fresh()
	if err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if got != "access-peer-refreshed" {
		t.Fatalf("got token %q, want peer-refreshed token", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("oauth.Refresh called %d times, want 0 (peer already refreshed)", n)
	}
}

// TestCredStateReloadErrorFallsBack verifies that when the credential
// file can no longer be read (permissions revoked mid-session, for
// example), Fresh falls back to the in-memory copy rather than
// failing the request.
func TestCredStateReloadErrorFallsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file-mode permission checks")
	}
	setupFakeHome(t)
	cred := makeCred(t, "66666666-6666-6666-6666-666666666666", "access-held")

	s := newCredState(cred)
	// Prime mtime.
	if _, err := s.Fresh(); err != nil {
		t.Fatalf("Fresh (prime): %v", err)
	}

	// Make the credential file unreadable, then bump its mtime so the
	// stat check inside Fresh decides to reload. The reload (os.ReadFile
	// inside store.Load) should fail, and Fresh should return the
	// in-memory token anyway.
	path := store.CredPath(cred.ID)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := s.Fresh()
	if err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if got != "access-held" {
		t.Fatalf("got token %q, want %q", got, "access-held")
	}
}
