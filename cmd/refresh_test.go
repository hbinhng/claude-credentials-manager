package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// seedRefreshCred writes a minimal credential file so store.Resolve
// can find it by prefix/name. Returns the full credential ID.
func seedRefreshCred(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".ccm"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	id := "refresh00-0000-0000-0000-00000000abcd"
	body := fmt.Sprintf(`{"id":%q,"name":%q,"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":%d,"scopes":["user:inference"]},"subscription":{"tier":"Claude Pro"},"createdAt":"2026-01-01T00:00:00Z","lastRefreshedAt":"2026-01-01T00:00:00Z"}`,
		id, name, time.Now().Add(-1*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(home, ".ccm", id+".credentials.json"), []byte(body), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

func stubRefreshFn(fn func(string) (*store.Credential, error)) func() {
	orig := refreshCredentialFn
	refreshCredentialFn = fn
	return func() { refreshCredentialFn = orig }
}

func TestDoRefreshCredential_HappyPath(t *testing.T) {
	id := seedRefreshCred(t, "work")
	defer stubRefreshFn(func(got string) (*store.Credential, error) {
		if got != id {
			t.Errorf("refreshCredentialFn got %q, want %q", got, id)
		}
		return &store.Credential{
			ID:   id,
			Name: "work",
			ClaudeAiOauth: store.OAuthTokens{
				AccessToken: "new",
				ExpiresAt:   time.Now().Add(1 * time.Hour).UnixMilli(),
			},
		}, nil
	})()

	var printed []string
	cred, err := doRefreshCredential("work", func(f string, args ...any) (int, error) {
		printed = append(printed, fmt.Sprintf(f, args...))
		return 0, nil
	})
	if err != nil {
		t.Fatalf("doRefreshCredential: %v", err)
	}
	if cred.ClaudeAiOauth.AccessToken != "new" {
		t.Errorf("AccessToken=%q", cred.ClaudeAiOauth.AccessToken)
	}
	if len(printed) != 2 {
		t.Errorf("printf called %d times, want 2", len(printed))
	}
	if !strings.Contains(printed[0], "Refreshing work") {
		t.Errorf("first printf=%q, want 'Refreshing work…'", printed[0])
	}
	if !strings.Contains(printed[1], "Refreshed. Old expiry") {
		t.Errorf("second printf=%q, want 'Refreshed. Old expiry…'", printed[1])
	}
}

func TestDoRefreshCredential_ResolveError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".ccm"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Store is empty; any identity resolves to not-found.
	defer stubRefreshFn(func(string) (*store.Credential, error) {
		t.Fatalf("refreshCredentialFn called; expected Resolve to fail first")
		return nil, nil
	})()

	_, err := doRefreshCredential("missing", func(string, ...any) (int, error) { return 0, nil })
	if err == nil {
		t.Fatalf("expected error for missing identity")
	}
}

func TestDoRefreshCredential_RefreshError(t *testing.T) {
	seedRefreshCred(t, "fails")
	defer stubRefreshFn(func(string) (*store.Credential, error) {
		return nil, errors.New("refresh boom")
	})()

	_, err := doRefreshCredential("fails", func(string, ...any) (int, error) { return 0, nil })
	if err == nil || !strings.Contains(err.Error(), "refresh boom") {
		t.Errorf("err=%v, want refresh boom", err)
	}
}

// stubActive swaps the claudeIsActiveFn / claudeWriteActiveFn seams.
func stubActive(active bool, writeErr error) func() {
	origIs, origW := claudeIsActiveFn, claudeWriteActiveFn
	claudeIsActiveFn = func(string) bool { return active }
	claudeWriteActiveFn = func(*store.Credential) error { return writeErr }
	return func() {
		claudeIsActiveFn = origIs
		claudeWriteActiveFn = origW
	}
}

func TestRefreshCredential_NonActive(t *testing.T) {
	id := seedRefreshCred(t, "nonactive")
	defer stubRefreshFn(func(string) (*store.Credential, error) {
		return &store.Credential{
			ID: id, Name: "nonactive",
			ClaudeAiOauth: store.OAuthTokens{ExpiresAt: time.Now().Add(1 * time.Hour).UnixMilli()},
		}, nil
	})()
	defer stubActive(false, nil)()

	if err := refreshCredential("nonactive"); err != nil {
		t.Errorf("refreshCredential: %v", err)
	}
}

func TestRefreshCredential_ActiveWriteSuccess(t *testing.T) {
	id := seedRefreshCred(t, "active")
	defer stubRefreshFn(func(string) (*store.Credential, error) {
		return &store.Credential{
			ID: id, Name: "active",
			ClaudeAiOauth: store.OAuthTokens{ExpiresAt: time.Now().Add(1 * time.Hour).UnixMilli()},
		}, nil
	})()
	calls := 0
	origIs, origW := claudeIsActiveFn, claudeWriteActiveFn
	claudeIsActiveFn = func(string) bool { return true }
	claudeWriteActiveFn = func(*store.Credential) error { calls++; return nil }
	defer func() { claudeIsActiveFn = origIs; claudeWriteActiveFn = origW }()

	if err := refreshCredential("active"); err != nil {
		t.Errorf("refreshCredential: %v", err)
	}
	if calls != 1 {
		t.Errorf("WriteActive called %d times, want 1", calls)
	}
}

func TestRefreshCredential_ActiveWriteError(t *testing.T) {
	id := seedRefreshCred(t, "active-err")
	defer stubRefreshFn(func(string) (*store.Credential, error) {
		return &store.Credential{
			ID: id, Name: "active-err",
			ClaudeAiOauth: store.OAuthTokens{ExpiresAt: time.Now().Add(1 * time.Hour).UnixMilli()},
		}, nil
	})()
	defer stubActive(true, errors.New("write boom"))()

	// WriteActive error is swallowed with a warning — refreshCredential
	// still returns nil because the OAuth token was persisted to the
	// store successfully.
	if err := refreshCredential("active-err"); err != nil {
		t.Errorf("refreshCredential: %v, want nil (warning only)", err)
	}
}

func TestRefreshCredential_DoRefreshError(t *testing.T) {
	seedRefreshCred(t, "active")
	defer stubRefreshFn(func(string) (*store.Credential, error) {
		return nil, errors.New("inner boom")
	})()

	if err := refreshCredential("active"); err == nil || !strings.Contains(err.Error(), "inner boom") {
		t.Errorf("err=%v, want inner boom", err)
	}
}

// ---------------------------------------------------------------------------
// Codex cmd-layer tests (Task 19)
// ---------------------------------------------------------------------------

// TestRefreshCmd_Codex_HappyPath verifies that refreshCredential round-trips
// correctly for a codex credential end-to-end (store → credflow → store).
func TestRefreshCmd_Codex_HappyPath(t *testing.T) {
	setupFakeHome(t)
	cred := mkCodexCredHelper(t, "id-codex")
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	cleanup := credflow.SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return &codexoauth.TokenResponse{
			AccessToken:  "new_a",
			RefreshToken: "new_r",
			IDToken:      "new_i",
		}, nil
	})
	defer cleanup()

	// refreshCredential uses fmt.Printf internally, so capture real stdout.
	out := captureStdout(t, func() {
		if err := refreshCredential(cred.ID); err != nil {
			t.Fatalf("refreshCredential: %v", err)
		}
	})
	if !strings.Contains(out, "Refreshed") {
		t.Fatalf("output should mention Refreshed: %s", out)
	}

	got, err := store.Load(cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tokens.AccessToken != "new_a" || got.Tokens.RefreshToken != "new_r" {
		t.Fatalf("rotation not persisted: %+v", got.Tokens)
	}
}

// TestRefreshCmd_Codex_BrickedRefreshToken_FriendlyError checks that when the
// codex refresh token has been rotated away, the cmd layer surfaces a friendly
// error message directing the user to re-authenticate.
func TestRefreshCmd_Codex_BrickedRefreshToken_FriendlyError(t *testing.T) {
	setupFakeHome(t)
	cred := mkCodexCredHelper(t, "id-bricked")
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	cleanup := credflow.SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return nil, codexoauth.ErrRefreshRotated
	})
	defer cleanup()

	err := refreshCredential(cred.ID)
	if err == nil || !strings.Contains(err.Error(), "ccm login codex") {
		t.Fatalf("want bricked error mentioning ccm login codex; got %v", err)
	}
}

// TestRefreshAll_MixedProviders_OneCodexFailureDoesNotBlockClaude confirms that
// when --all is used with mixed providers, a codex failure does not prevent the
// claude credential from refreshing.
func TestRefreshAll_MixedProviders_OneCodexFailureDoesNotBlockClaude(t *testing.T) {
	setupFakeHome(t)
	claudeCred := mkClaudeCredHelper(t, "claude1")
	if err := store.Save(claudeCred); err != nil {
		t.Fatal(err)
	}
	codexCred := mkCodexCredHelper(t, "codex1")
	if err := store.Save(codexCred); err != nil {
		t.Fatal(err)
	}

	// Stub refreshCredentialFn to dispatch by provider: codex fails, claude succeeds.
	restore := stubRefreshFn(func(id string) (*store.Credential, error) {
		c, err := store.Load(id)
		if err != nil {
			return nil, err
		}
		if c.ProviderName() == "codex" {
			return nil, codexoauth.ErrTokenEndpoint
		}
		// claude: return a refreshed copy.
		c.ClaudeAiOauth.AccessToken = "refreshed_claude"
		c.ClaudeAiOauth.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
		return c, nil
	})
	defer restore()

	// Also stub claudeSyncFn to avoid touching real ~/.claude.
	origSync := claudeSyncFn
	claudeSyncFn = func() (bool, error) { return false, nil }
	defer func() { claudeSyncFn = origSync }()

	out := captureStdout(t, func() {
		// refreshAllCredentials() ignores partial failures in its return value
		// only when all fail. Here it returns an error (1 of 2 failed) but we
		// still want to check that both creds appeared in the output.
		_ = refreshAllCredentials()
	})

	if !strings.Contains(out, "claude1") {
		t.Fatalf("expected mention of claude1 in output: %s", out)
	}
	if !strings.Contains(out, "codex1") {
		t.Fatalf("expected mention of codex1 in output: %s", out)
	}
}

// TestRefreshAll_AllCodex_FullySupported verifies that --all works when all
// credentials are codex-provider.
func TestRefreshAll_AllCodex_FullySupported(t *testing.T) {
	setupFakeHome(t)
	x1 := mkCodexCredHelper(t, "x1")
	if err := store.Save(x1); err != nil {
		t.Fatal(err)
	}
	x2 := mkCodexCredHelper(t, "x2")
	if err := store.Save(x2); err != nil {
		t.Fatal(err)
	}

	cleanup := credflow.SeamCodexRefresh(func(string) (*codexoauth.TokenResponse, error) {
		return &codexoauth.TokenResponse{AccessToken: "a", RefreshToken: "r", IDToken: "i"}, nil
	})
	defer cleanup()

	out := captureStdout(t, func() {
		if err := refreshAllCredentials(); err != nil {
			t.Fatalf("refreshAllCredentials: %v", err)
		}
	})
	if !strings.Contains(out, "x1") || !strings.Contains(out, "x2") {
		t.Fatalf("expected both codex creds in output: %s", out)
	}
}
