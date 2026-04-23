package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// seedRefreshCred writes a minimal credential file so store.Resolve
// can find it by prefix/name. Returns the full credential ID.
func seedRefreshCred(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
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
