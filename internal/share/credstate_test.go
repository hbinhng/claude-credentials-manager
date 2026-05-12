package share

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
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

	s, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
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

	s, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
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

	s, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
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

	s, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
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

	s, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
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
// mkCodexCred builds a minimal codex credential for tests that need a
// non-claude provider.
func mkCodexCred(t *testing.T, id string) *store.Credential {
	t.Helper()
	return mkCodexCredWithExp(t, id, time.Now().Add(time.Hour).Unix())
}

// mkExpiredCodexCred builds a minimal codex credential whose JWT is
// already expired.
func mkExpiredCodexCred(t *testing.T, id string) *store.Credential {
	t.Helper()
	return mkCodexCredWithExp(t, id, time.Now().Add(-time.Second).Unix())
}

// mkCodexCredWithExp constructs a codex credential whose access/id tokens
// carry the given Unix second timestamp as the JWT exp claim.
func mkCodexCredWithExp(t *testing.T, id string, expUnix int64) *store.Credential {
	t.Helper()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(
		`{"email":"u@x.com","exp":` + strconv.FormatInt(expUnix, 10) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`,
	))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	tok := h + "." + p + "." + s
	return &store.Credential{
		ID: id, Name: "codex-test", Provider: "codex",
		AuthMode: "chatgpt", OpenAIAPIKey: nil,
		Tokens:          &store.CodexTokens{IDToken: tok, AccessToken: tok, RefreshToken: "rt_a.b", AccountID: "acct"},
		LastRefresh:     "2026-05-08T00:00:00Z",
		CreatedAt:       "2026-05-08T00:00:00Z",
		LastRefreshedAt: "2026-05-08T00:00:00Z",
	}
}

func TestCredStateNew_AcceptsCodexCred(t *testing.T) {
	setupFakeHome(t)
	cred := mkCodexCred(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	_, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: unexpected error for codex cred: %v", err)
	}
}

func TestCredStateReloadErrorFallsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file-mode permission checks")
	}
	setupFakeHome(t)
	cred := makeCred(t, "66666666-6666-6666-6666-666666666666", "access-held")

	s, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
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

func TestCredState_Codex_FreshReturnsAccessToken(t *testing.T) {
	setupFakeHome(t)
	cred := mkCodexCred(t, "00000000-0000-0000-0000-000000000002")
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	// Reload from disk so expiresAtMillis cache is populated.
	loaded, err := store.Load(cred.ID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	cs, err := newCredState(loaded)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
	tok, err := cs.Fresh()
	if err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if tok != loaded.AccessToken() {
		t.Errorf("credState returned %q, want cred.AccessToken() = %q", tok, loaded.AccessToken())
	}
}

func TestCredState_Codex_RefreshViaCredflow(t *testing.T) {
	setupFakeHome(t)
	credID := "00000000-0000-0000-0000-000000000003"
	// Build a codex cred whose JWT is already expired so Fresh() will
	// take the slow path and invoke credflow.RefreshFn.
	cred := mkExpiredCodexCred(t, credID)
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	called := false
	restore := credflow.SetRefreshFnForTest(func(id string) (*store.Credential, error) {
		called = true
		fresh := mkCodexCred(t, id)
		fresh.Tokens.AccessToken = "acc-refreshed"
		_ = store.Save(fresh)
		return fresh, nil
	})
	defer restore()

	loaded, err := store.Load(credID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	cs, err := newCredState(loaded)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
	tok, err := cs.Fresh()
	if err != nil {
		t.Fatalf("Fresh triggered refresh: %v", err)
	}
	if !called {
		t.Error("credflow.RefreshFn was not invoked; credstate still uses inline oauth.Refresh")
	}
	if tok != "acc-refreshed" {
		t.Errorf("Fresh returned %q, want acc-refreshed", tok)
	}
}

func TestCredState_Claude_FreshUnchanged(t *testing.T) {
	// Regression: existing claude path must continue to work after the
	// refactor. peer-reload semantics preserved.
	setupFakeHome(t)
	cred := makeCred(t, "00000000-0000-0000-0000-000000000004", "claude-acc-tok")
	cs, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
	tok, err := cs.Fresh()
	if err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if tok == "" {
		t.Error("Fresh returned empty token")
	}
	if tok != "claude-acc-tok" {
		t.Errorf("Fresh returned %q, want %q", tok, "claude-acc-tok")
	}
}

func TestCredStateUpstreamURL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CCM_HOME", tmp)
	_ = os.MkdirAll(filepath.Join(tmp, ".ccm"), 0o700)

	cred := &store.Credential{ID: "abc", ClaudeAiOauth: store.OAuthTokens{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}}
	state, err := newCredState(cred)
	if err != nil {
		t.Fatalf("newCredState: %v", err)
	}
	if got := state.upstreamURL(); got != upstreamBase() {
		t.Errorf("upstreamURL() = %q, want %q", got, upstreamBase())
	}
	if state.isPassthrough() {
		t.Errorf("credState.isPassthrough() = true, want false")
	}
}
