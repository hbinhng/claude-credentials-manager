package credflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	grokoauth "github.com/hbinhng/claude-credentials-manager/internal/grok/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestRefreshGrokLocked_RotatesAndPersists(t *testing.T) {
	restore := SeamGrokRefresh(func(rt string) (*grokoauth.TokenResponse, error) {
		if rt != "old-refresh" {
			t.Errorf("refresh token = %q", rt)
		}
		return &grokoauth.TokenResponse{AccessToken: "new-acc", RefreshToken: "new-refresh", ExpiresIn: 3600}, nil
	})
	defer restore()

	c := &store.Credential{ID: "g", Provider: "grok", GrokTokens: &store.GrokTokens{AccessToken: "old-acc", RefreshToken: "old-refresh"}}
	c.SetTokens("old-acc", "old-refresh", time.Now().Add(-time.Minute).UnixMilli())

	// refreshGrokLocked calls store.Save; point HOME at a temp dir.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	out, err := refreshGrokLocked(c)
	if err != nil {
		t.Fatalf("refreshGrokLocked: %v", err)
	}
	if out.AccessToken() != "new-acc" || out.RefreshToken() != "new-refresh" {
		t.Fatalf("tokens not rotated: %q/%q", out.AccessToken(), out.RefreshToken())
	}
	if out.IsExpired() {
		t.Fatal("expiry not advanced")
	}
}

func TestRefreshGrokLocked_PropagatesRotated(t *testing.T) {
	restore := SeamGrokRefresh(func(string) (*grokoauth.TokenResponse, error) {
		return nil, grokoauth.ErrRefreshRotated
	})
	defer restore()
	c := &store.Credential{ID: "g", Provider: "grok", GrokTokens: &store.GrokTokens{AccessToken: "a", RefreshToken: "r"}}
	_, err := refreshGrokLocked(c)
	if !errors.Is(err, grokoauth.ErrRefreshRotated) {
		t.Fatalf("want ErrRefreshRotated, got %v", err)
	}
}

func TestRefreshGrokLocked_MissingTokens_FriendlyError(t *testing.T) {
	// Covers the nil GrokTokens / empty RefreshToken guard directly.
	c := &store.Credential{ID: "g", Provider: "grok", GrokTokens: nil}
	_, err := refreshGrokLocked(c)
	if err == nil || !strings.Contains(err.Error(), "missing tokens") || !strings.Contains(err.Error(), "ccm login grok") {
		t.Fatalf("want missing-tokens error mentioning ccm login grok; got %v", err)
	}

	c2 := &store.Credential{ID: "g", Provider: "grok", GrokTokens: &store.GrokTokens{AccessToken: "a", RefreshToken: ""}}
	_, err = refreshGrokLocked(c2)
	if err == nil || !strings.Contains(err.Error(), "missing tokens") {
		t.Fatalf("want missing-tokens error for empty refresh token; got %v", err)
	}
}

func TestRefreshCredential_Grok_HappyPath(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Name:     "n",
		Provider: "grok",
		GrokTokens: &store.GrokTokens{
			AccessToken:  "a_old",
			RefreshToken: "r_old",
		},
		LastRefresh:     "old",
		LastRefreshedAt: "old",
		CreatedAt:       "t",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	cleanup := SeamGrokRefresh(func(string) (*grokoauth.TokenResponse, error) {
		return &grokoauth.TokenResponse{
			AccessToken:  "a_new",
			RefreshToken: "r_new",
			ExpiresIn:    3600,
		}, nil
	})
	defer cleanup()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatal(err)
	}
	if out.GrokTokens.AccessToken != "a_new" || out.GrokTokens.RefreshToken != "r_new" {
		t.Fatalf("rotation not applied: %+v", out.GrokTokens)
	}
	if out.LastRefresh == "old" {
		t.Fatal("LastRefresh not updated")
	}
}

func TestRefreshCredential_Grok_TokensNil_FriendlyError(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:         "abc",
		Provider:   "grok",
		GrokTokens: nil,
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	_, err := RefreshCredential("abc")
	if err == nil || !strings.Contains(err.Error(), "missing tokens") || !strings.Contains(err.Error(), "ccm login grok") {
		t.Fatalf("want missing-tokens error mentioning ccm login grok; got %v", err)
	}
}

func TestRefreshCredential_Grok_GenuinelyBricked(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "grok",
		GrokTokens: &store.GrokTokens{
			AccessToken:  "a",
			RefreshToken: "r",
		},
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	cleanup := SeamGrokRefresh(func(string) (*grokoauth.TokenResponse, error) {
		return nil, grokoauth.ErrRefreshRotated
	})
	defer cleanup()

	_, err := RefreshCredential("abc")
	if err == nil || !strings.Contains(err.Error(), "ccm login grok") {
		t.Fatalf("want bricked error mentioning ccm login grok; got %v", err)
	}
}

func TestRefreshCredential_Grok_RotatedThenDiskWinner(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "grok",
		GrokTokens: &store.GrokTokens{
			AccessToken:  "a_old",
			RefreshToken: "r",
		},
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	cleanup := SeamGrokRefresh(func(string) (*grokoauth.TokenResponse, error) {
		// Before our exchange "fails", another process writes a new token to disk.
		c, _ := store.Load("abc")
		c.GrokTokens.AccessToken = "a_winner"
		_ = store.Save(c)
		return nil, grokoauth.ErrRefreshRotated
	})
	defer cleanup()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatalf("expected silent success on cross-process win; got %v", err)
	}
	if out.GrokTokens.AccessToken != "a_winner" {
		t.Fatalf("expected disk version; got %q", out.GrokTokens.AccessToken)
	}
}

func TestRefreshCredential_Grok_EmptyRefreshTokenKeepsOld(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:       "abc",
		Provider: "grok",
		GrokTokens: &store.GrokTokens{
			AccessToken:  "a_old",
			RefreshToken: "r_old",
		},
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	cleanup := SeamGrokRefresh(func(string) (*grokoauth.TokenResponse, error) {
		return &grokoauth.TokenResponse{AccessToken: "a_new", RefreshToken: "", ExpiresIn: 3600}, nil
	})
	defer cleanup()

	out, err := RefreshCredential("abc")
	if err != nil {
		t.Fatal(err)
	}
	if out.GrokTokens.RefreshToken != "r_old" {
		t.Fatalf("want old refresh token kept, got %q", out.GrokTokens.RefreshToken)
	}
	if out.GrokTokens.AccessToken != "a_new" {
		t.Fatalf("want new access token, got %q", out.GrokTokens.AccessToken)
	}
}

func TestRefreshCredential_Grok_SaveError(t *testing.T) {
	setupFakeHome(t, "abc")
	cred := &store.Credential{
		ID:         "abc",
		Provider:   "grok",
		GrokTokens: &store.GrokTokens{AccessToken: "a", RefreshToken: "r"},
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
	cleanup := SeamGrokRefresh(func(string) (*grokoauth.TokenResponse, error) {
		// Strip write permission right before Save is called.
		_ = os.Chmod(ccmDir, 0o500)
		return &grokoauth.TokenResponse{AccessToken: "a_new", RefreshToken: "r_new"}, nil
	})
	defer cleanup()
	t.Cleanup(func() { _ = os.Chmod(ccmDir, 0o700) })

	_, err := RefreshCredential("abc")
	if err == nil {
		t.Fatal("expected save error; got nil")
	}
}

func TestAccessTokenDiffers_GrokNilTokens(t *testing.T) {
	// Covers the nil-GrokTokens guard in accessTokenDiffers (both branches).
	diskNil := &store.Credential{Provider: "grok", GrokTokens: nil}
	memNil := &store.Credential{Provider: "grok", GrokTokens: nil}
	memHas := &store.Credential{Provider: "grok", GrokTokens: &store.GrokTokens{AccessToken: "a"}}
	diskHas := &store.Credential{Provider: "grok", GrokTokens: &store.GrokTokens{AccessToken: "b"}}

	if accessTokenDiffers(diskNil, memHas) {
		t.Error("diskNil vs memHas: expected false (can't compare)")
	}
	if accessTokenDiffers(diskHas, memNil) {
		t.Error("diskHas vs memNil: expected false (can't compare)")
	}
	if accessTokenDiffers(diskHas, diskHas) {
		t.Error("same pointer: expected false")
	}
}
