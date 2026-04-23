package credflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
