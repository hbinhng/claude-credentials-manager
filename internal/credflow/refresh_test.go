package credflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// setupFakeHome points HOME at a tempdir so store.Load/Save touch a
// fresh ~/.ccm/ per test. Returns the credential ID that was seeded.
func setupFakeHome(t *testing.T, credID string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".ccm"), 0700); err != nil {
		t.Fatalf("mkdir .ccm: %v", err)
	}
	expires := time.Now().Add(-1 * time.Hour).UnixMilli() // expired on purpose
	body := []byte(`{
      "id": "` + credID + `",
      "name": "test",
      "claudeAiOauth": {
        "accessToken": "old-access",
        "refreshToken": "old-refresh",
        "expiresAt": ` + itoa(expires) + `,
        "scopes": ["user:inference"]
      },
      "subscription": {"tier": "Claude Pro"},
      "createdAt": "2026-01-01T00:00:00Z",
      "lastRefreshedAt": "2026-01-01T00:00:00Z"
    }`)
	if err := os.WriteFile(filepath.Join(home, ".ccm", credID+".credentials.json"), body, 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return credID
}

func itoa(n int64) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if s == "" {
		return "0"
	}
	return s
}

func stubSeams(refresh func(string) (*oauth.TokenResponse, error), profile func(string) oauth.Profile) func() {
	origRefresh, origProfile := oauthRefreshFn, oauthFetchProfileFn
	oauthRefreshFn = refresh
	oauthFetchProfileFn = profile
	return func() {
		oauthRefreshFn = origRefresh
		oauthFetchProfileFn = origProfile
	}
}

func TestRefreshCredential_HappyPath(t *testing.T) {
	id := setupFakeHome(t, "4300c4bc-c04d-4b1f-8609-6c7b518de3df")

	defer stubSeams(
		func(rt string) (*oauth.TokenResponse, error) {
			if rt != "old-refresh" {
				t.Errorf("refresh called with %q, want old-refresh", rt)
			}
			return &oauth.TokenResponse{
				AccessToken:  "new-access",
				RefreshToken: "new-refresh",
				ExpiresIn:    3600,
				Scope:        "user:inference user:profile",
			}, nil
		},
		func(at string) oauth.Profile {
			if at != "new-access" {
				t.Errorf("profile called with %q, want new-access", at)
			}
			return oauth.Profile{Tier: "Claude Max 20x"}
		},
	)()

	got, err := RefreshCredential(id)
	if err != nil {
		t.Fatalf("RefreshCredential: %v", err)
	}
	if got.ClaudeAiOauth.AccessToken != "new-access" {
		t.Errorf("AccessToken=%q", got.ClaudeAiOauth.AccessToken)
	}
	if got.ClaudeAiOauth.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken=%q", got.ClaudeAiOauth.RefreshToken)
	}
	if got.Subscription.Tier != "Claude Max 20x" {
		t.Errorf("Tier=%q", got.Subscription.Tier)
	}
	if len(got.ClaudeAiOauth.Scopes) != 2 {
		t.Errorf("Scopes=%v, want 2 items", got.ClaudeAiOauth.Scopes)
	}
	// Persisted to disk.
	reloaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if reloaded.ClaudeAiOauth.AccessToken != "new-access" {
		t.Errorf("on-disk AccessToken=%q", reloaded.ClaudeAiOauth.AccessToken)
	}
}

func TestRefreshCredential_EmptyScopeFallsBackToExisting(t *testing.T) {
	id := setupFakeHome(t, "scope-0000-0000-0000-0000-000000000001")
	defer stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "a", ExpiresIn: 60, Scope: ""}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)()
	got, err := RefreshCredential(id)
	if err != nil {
		t.Fatalf("RefreshCredential: %v", err)
	}
	if len(got.ClaudeAiOauth.Scopes) == 0 {
		t.Errorf("Scopes wiped when upstream returned empty; want original preserved")
	}
}

func TestRefreshCredential_EmptyRefreshTokenKeepsOld(t *testing.T) {
	id := setupFakeHome(t, "rt-0000-0000-0000-0000-000000000002")
	defer stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "a", ExpiresIn: 60, Scope: "x"}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)()
	got, err := RefreshCredential(id)
	if err != nil {
		t.Fatalf("RefreshCredential: %v", err)
	}
	if got.ClaudeAiOauth.RefreshToken != "old-refresh" {
		t.Errorf("RefreshToken=%q, want old-refresh preserved", got.ClaudeAiOauth.RefreshToken)
	}
}

func TestRefreshCredential_EmptyTierKeepsOld(t *testing.T) {
	id := setupFakeHome(t, "tier-0000-0000-0000-0000-000000000003")
	defer stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "a", ExpiresIn: 60, Scope: "x"}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{Tier: ""} },
	)()
	got, err := RefreshCredential(id)
	if err != nil {
		t.Fatalf("RefreshCredential: %v", err)
	}
	if got.Subscription.Tier != "Claude Pro" {
		t.Errorf("Tier overwritten to %q; want original preserved when profile empty", got.Subscription.Tier)
	}
}

func TestRefreshCredential_UnknownID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defer stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			t.Fatalf("refresh called; should have failed to load first")
			return nil, nil
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)()
	_, err := RefreshCredential("nope")
	if err == nil {
		t.Fatalf("expected error for unknown ID")
	}
}

func TestRefreshCredential_RefreshTokenExpired(t *testing.T) {
	id := setupFakeHome(t, "revoked-0000-0000-0000-000000000004")
	defer stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			return nil, errors.New("401 Unauthorized")
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)()
	_, err := RefreshCredential(id)
	if err == nil || !strings.Contains(err.Error(), "Re-authenticate") {
		t.Errorf("err=%v, want friendly 'Re-authenticate' message", err)
	}
}

func TestRefreshCredential_OtherOauthError(t *testing.T) {
	id := setupFakeHome(t, "down-0000-0000-0000-0000-000000000005")
	defer stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			return nil, errors.New("connection refused")
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)()
	_, err := RefreshCredential(id)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err=%v, want raw error passthrough", err)
	}
}

func TestRefreshCredential_SaveError(t *testing.T) {
	id := setupFakeHome(t, "save-0000-0000-0000-0000-000000000006")
	ccmDir := filepath.Join(os.Getenv("HOME"), ".ccm")
	// Pre-create the lock file so WithCredentialLock can open it even
	// after the directory loses its write bit. (Opening an existing file
	// only requires execute permission on the parent; creating a new file
	// requires write.)
	lockFile := filepath.Join(ccmDir, id+".credentials.json.lock")
	if err := os.WriteFile(lockFile, nil, 0o600); err != nil {
		t.Fatalf("pre-create lock file: %v", err)
	}
	// Keep the credential file readable (Load succeeds) but strip write
	// permission from the directory so store.Save can't create its
	// temporary file during the atomic rename. Restore permissions in
	// cleanup so t.TempDir() can remove the tree.
	if err := os.Chmod(ccmDir, 0o500); err != nil {
		t.Fatalf("chmod ccm dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ccmDir, 0o700) })

	defer stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "a", ExpiresIn: 60}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)()

	_, err := RefreshCredential(id)
	if err == nil || !strings.Contains(err.Error(), "save credentials") {
		t.Errorf("err=%v, want 'save credentials' in message", err)
	}
}

func TestRefreshCredential_HoldsLockForDuration(t *testing.T) {
	setupFakeHome(t, "abc")
	stop := make(chan struct{})
	cleanup := stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			<-stop
			return &oauth.TokenResponse{AccessToken: "new", RefreshToken: "new"}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)
	defer cleanup()

	first := make(chan error, 1)
	go func() { _, err := RefreshCredential("abc"); first <- err }()
	time.Sleep(80 * time.Millisecond)
	second := make(chan error, 1)
	go func() { _, err := RefreshCredential("abc"); second <- err }()
	select {
	case <-second:
		close(stop)
		<-first
		t.Fatal("second RefreshCredential returned before first finished")
	case <-time.After(150 * time.Millisecond):
	}
	close(stop)
	if err := <-first; err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second: %v", err)
	}
}

func TestRefreshCredential_DetectsCrossProcessRotation_BeforeExchange(t *testing.T) {
	setupFakeHome(t, "abc")
	called := false
	cleanup := stubSeams(
		func(string) (*oauth.TokenResponse, error) {
			called = true
			return &oauth.TokenResponse{AccessToken: "new", RefreshToken: "new"}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{} },
	)
	defer cleanup()

	SeamBetweenResolveAndLock = func(id string) {
		c, _ := store.Load(id)
		c.ClaudeAiOauth.AccessToken = "rewritten-by-other"
		_ = store.Save(c)
	}
	defer func() { SeamBetweenResolveAndLock = nil }()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("token endpoint hit despite cross-process rotation")
	}
	if out.ClaudeAiOauth.AccessToken != "rewritten-by-other" {
		t.Fatalf("did not return disk version: got %q", out.ClaudeAiOauth.AccessToken)
	}
}

func TestRefreshCredential_Codex_HappyPath(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Name:     "n",
		Provider: "codex",
		AuthMode: "chatgpt",
		Tokens: &store.CodexTokens{
			IDToken:      "i_old",
			AccessToken:  "a_old",
			RefreshToken: "r_old",
			AccountID:    "acct",
		},
		LastRefresh:     "old",
		LastRefreshedAt: "old",
		CreatedAt:       "t",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	cleanup := SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return &codexoauth.TokenResponse{
			AccessToken:  "a_new",
			RefreshToken: "r_new",
			IDToken:      "i_new",
		}, nil
	})
	defer cleanup()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatal(err)
	}
	if out.Tokens.AccessToken != "a_new" || out.Tokens.RefreshToken != "r_new" || out.Tokens.IDToken != "i_new" {
		t.Fatalf("rotation not applied: %+v", out.Tokens)
	}
	if out.LastRefresh == "old" {
		t.Fatal("LastRefresh not updated")
	}
}

func TestRefreshCredential_Codex_TokensNil_FriendlyError(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "codex",
		Tokens:   nil,
		AuthMode: "chatgpt",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	_, err := RefreshCredential("abc")
	if err == nil || !strings.Contains(err.Error(), "missing tokens") || !strings.Contains(err.Error(), "ccm login codex") {
		t.Fatalf("want missing-tokens error mentioning ccm login codex; got %v", err)
	}
}

func TestRefreshCredential_Codex_GenuinelyBricked(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "codex",
		Tokens: &store.CodexTokens{
			AccessToken:  "a",
			RefreshToken: "r",
		},
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	cleanup := SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return nil, codexoauth.ErrRefreshRotated
	})
	defer cleanup()

	_, err := RefreshCredential("abc")
	if err == nil || !strings.Contains(err.Error(), "ccm login codex") {
		t.Fatalf("want bricked error mentioning ccm login codex; got %v", err)
	}
}

func TestRefreshCredential_Codex_RotatedThenDiskWinner(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "codex",
		Tokens: &store.CodexTokens{
			AccessToken:  "a_old",
			RefreshToken: "r",
		},
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	cleanup := SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		// Before our exchange "fails", another process writes a new token to disk.
		c, _ := store.Load("abc")
		c.Tokens.AccessToken = "a_winner"
		_ = store.Save(c)
		return nil, codexoauth.ErrRefreshRotated
	})
	defer cleanup()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatalf("expected silent success on cross-process win; got %v", err)
	}
	if out.Tokens.AccessToken != "a_winner" {
		t.Fatalf("expected disk version; got %q", out.Tokens.AccessToken)
	}
}

func TestRefreshCredential_UnknownProvider_Errors(t *testing.T) {
	// untestable: store.UnmarshalJSON rejects unknown providers before reaching this switch
	t.Skip("unknown-provider path is unreachable through normal ingestion (UnmarshalJSON rejects)")
}

func TestRefreshCredential_Codex_SaveError(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "codex",
		Tokens:   &store.CodexTokens{AccessToken: "a", RefreshToken: "r"},
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	ccmDir := filepath.Join(os.Getenv("HOME"), ".ccm")
	// Pre-create the lock file so WithCredentialLock can open it
	// even after the directory loses its write bit.
	lockFile := filepath.Join(ccmDir, "abc.credentials.json.lock")
	if err := os.WriteFile(lockFile, nil, 0o600); err != nil {
		t.Fatalf("pre-create lock file: %v", err)
	}
	cleanup := SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		// Strip write permission right before Save is called.
		_ = os.Chmod(ccmDir, 0o500)
		return &codexoauth.TokenResponse{AccessToken: "a_new", RefreshToken: "r_new"}, nil
	})
	defer cleanup()
	t.Cleanup(func() { _ = os.Chmod(ccmDir, 0o700) })

	_, err := RefreshCredential("abc")
	if err == nil {
		t.Fatal("expected save error; got nil")
	}
}

func TestAccessTokenDiffers_CodexNilTokens(t *testing.T) {
	// Covers the nil-Tokens guard in accessTokenDiffers (both branches).
	diskNil := &store.Credential{Provider: "codex", Tokens: nil}
	memNil := &store.Credential{Provider: "codex", Tokens: nil}
	memHas := &store.Credential{Provider: "codex", Tokens: &store.CodexTokens{AccessToken: "a"}}
	diskHas := &store.Credential{Provider: "codex", Tokens: &store.CodexTokens{AccessToken: "b"}}

	if accessTokenDiffers(diskNil, memHas) {
		t.Error("diskNil vs memHas: expected false (can't compare)")
	}
	if accessTokenDiffers(diskHas, memNil) {
		t.Error("diskHas vs memNil: expected false (can't compare)")
	}
	// Same tokens — no difference.
	if accessTokenDiffers(diskHas, diskHas) {
		t.Error("same pointer: expected false")
	}
}

func TestRefreshCredential_LockLoadError(t *testing.T) {
	// Cover store.Load error path inside the lock: delete the credential
	// file after Resolve (via SeamBetweenResolveAndLock) so Load fails.
	setupFakeHome(t, "abc")
	SeamBetweenResolveAndLock = func(id string) {
		ccmDir := filepath.Join(os.Getenv("HOME"), ".ccm")
		_ = os.Remove(filepath.Join(ccmDir, id+".credentials.json"))
	}
	defer func() { SeamBetweenResolveAndLock = nil }()

	_, err := RefreshCredential("abc")
	if err == nil {
		t.Fatal("expected error when credential file deleted inside lock")
	}
}

func TestRefreshCredential_Codex_UpdatesTierFromUsage(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Name:     "n",
		Provider: "codex",
		AuthMode: "chatgpt",
		Tokens: &store.CodexTokens{
			IDToken:      "i_old",
			AccessToken:  "a_old",
			RefreshToken: "r_old",
			AccountID:    "acct",
		},
		LastRefresh:     "old",
		LastRefreshedAt: "old",
		CreatedAt:       "t",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	cleanupRefresh := SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return &codexoauth.TokenResponse{
			AccessToken:  "a_new",
			RefreshToken: "r_new",
			IDToken:      "i_new",
		}, nil
	})
	defer cleanupRefresh()

	cleanupUsage := SeamCodexUsage(func(at, acct string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Tier: "Pro", Quotas: []oauth.Quota{{Name: "5h", Used: 10}}}
	})
	defer cleanupUsage()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatalf("RefreshCredential: %v", err)
	}
	if out.Subscription.Tier != "Pro" {
		t.Errorf("Tier = %q, want Pro", out.Subscription.Tier)
	}
	// Verify persisted to disk.
	reloaded, err := store.Load("abc")
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if reloaded.Subscription.Tier != "Pro" {
		t.Errorf("on-disk Tier = %q, want Pro", reloaded.Subscription.Tier)
	}
}

func TestRefreshCredential_Codex_UsageFailure_RefreshStillSucceeds(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Name:     "n",
		Provider: "codex",
		AuthMode: "chatgpt",
		Tokens: &store.CodexTokens{
			IDToken:      "i_old",
			AccessToken:  "a_old",
			RefreshToken: "r_old",
			AccountID:    "acct",
		},
		Subscription:    store.Subscription{Tier: "OldTier"},
		LastRefresh:     "old",
		LastRefreshedAt: "old",
		CreatedAt:       "t",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	cleanupRefresh := SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return &codexoauth.TokenResponse{AccessToken: "a_new", RefreshToken: "r_new"}, nil
	})
	defer cleanupRefresh()

	// Usage fetch fails — error non-empty, Tier empty.
	cleanupUsage := SeamCodexUsage(func(at, acct string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Error: "HTTP 503"}
	})
	defer cleanupUsage()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatalf("RefreshCredential should succeed even when usage fails: %v", err)
	}
	if out.Tokens.AccessToken != "a_new" {
		t.Errorf("AccessToken = %q, want a_new", out.Tokens.AccessToken)
	}
	// Tier not updated when usage has empty Tier.
	if out.Subscription.Tier != "OldTier" {
		t.Errorf("Tier = %q, want OldTier preserved", out.Subscription.Tier)
	}
}

func TestRefreshCredential_Codex_UsageNil_RefreshStillSucceeds(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "codex",
		AuthMode: "chatgpt",
		Tokens:   &store.CodexTokens{AccessToken: "a_old", RefreshToken: "r_old"},
		CreatedAt: "t", LastRefreshedAt: "t",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	cleanupRefresh := SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return &codexoauth.TokenResponse{AccessToken: "a_new", RefreshToken: "r_new"}, nil
	})
	defer cleanupRefresh()

	// Usage returns nil (should not panic).
	cleanupUsage := SeamCodexUsage(func(at, acct string) *oauth.UsageInfo {
		return nil
	})
	defer cleanupUsage()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatalf("RefreshCredential should succeed even when usage returns nil: %v", err)
	}
	if out.Tokens.AccessToken != "a_new" {
		t.Errorf("AccessToken = %q, want a_new", out.Tokens.AccessToken)
	}
}
