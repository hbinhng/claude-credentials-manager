package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func setupHomeWithCcm(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".ccm"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(claude.UseFileBackendForTest())
	return home
}

func saveCredFor(t *testing.T, id string, tokens store.OAuthTokens) *store.Credential {
	t.Helper()
	cred := &store.Credential{
		ID:              id,
		Name:            "named-" + id,
		ClaudeAiOauth:   tokens,
		CreatedAt:       "2026-01-01T00:00:00Z",
		LastRefreshedAt: "2026-01-01T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	return cred
}

// installActiveBlob writes a {ccmSourceId, claudeAiOauth} blob directly
// into ~/.claude/.credentials.json. Tests use this to simulate Claude
// having written a fresh blob (with our marker preserved by Claude's
// round-trip behavior).
func installActiveBlob(t *testing.T, id string, tokens store.OAuthTokens) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"ccmSourceId":   id,
		"claudeAiOauth": tokens,
	})
	target := filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	if err := os.WriteFile(target, body, 0600); err != nil {
		t.Fatal(err)
	}
}

// writePlainClaudeBlob writes a Claude blob WITHOUT our marker. Use to
// simulate the "Claude has credentials but ccm hasn't activated" case.
func writePlainClaudeBlob(t *testing.T, body string) {
	t.Helper()
	target := filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	if err := os.WriteFile(target, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRunBackup_MissingClaudeFileErrors(t *testing.T) {
	setupHomeWithCcm(t)
	if err := runBackup(); err == nil {
		t.Error("runBackup: nil err, want missing-file error")
	}
}

func TestRunBackup_ActiveBlob_SyncBranch_ClaudeNewer(t *testing.T) {
	setupHomeWithCcm(t)
	cred := saveCredFor(t, "active-id", store.OAuthTokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 1000, Scopes: []string{"user:inference"}})
	installActiveBlob(t, "active-id", store.OAuthTokens{AccessToken: "fresh", RefreshToken: "r2", ExpiresAt: time.Now().Add(1 * time.Hour).UnixMilli(), Scopes: []string{"user:inference"}})

	if err := runBackup(); err != nil {
		t.Fatalf("runBackup: %v", err)
	}

	loaded, err := store.Load(cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ClaudeAiOauth.AccessToken != "fresh" {
		t.Errorf("AccessToken = %q, want fresh", loaded.ClaudeAiOauth.AccessToken)
	}

	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("store has %d creds, want 1", len(all))
	}
}

func TestRunBackup_ActiveBlob_SyncBranch_NoChange(t *testing.T) {
	setupHomeWithCcm(t)
	cred := saveCredFor(t, "noop", store.OAuthTokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 5000, Scopes: []string{"user:inference"}})
	installActiveBlob(t, cred.ID, cred.ClaudeAiOauth)

	if err := runBackup(); err != nil {
		t.Fatalf("runBackup: %v", err)
	}
	all, _ := store.List()
	if len(all) != 1 {
		t.Errorf("store has %d, want 1 (no duplicate)", len(all))
	}
}

func TestRunBackup_ActiveBlob_StoreMissing_FallsThrough(t *testing.T) {
	setupHomeWithCcm(t)
	installActiveBlob(t, "ghost", store.OAuthTokens{AccessToken: "new", RefreshToken: "r", ExpiresAt: 1, Scopes: []string{"user:inference"}})

	origProfile := backupFetchProfileFn
	backupFetchProfileFn = func(string) backupProfile { return backupProfile{Email: "x@y", Tier: "T"} }
	defer func() { backupFetchProfileFn = origProfile }()

	if err := runBackup(); err != nil {
		t.Fatalf("runBackup: %v", err)
	}
	all, _ := store.List()
	if len(all) != 1 {
		t.Errorf("store has %d creds, want 1 (newly imported)", len(all))
	}
}

func TestRunBackup_NoActive_ImportsAsNew(t *testing.T) {
	setupHomeWithCcm(t)
	writePlainClaudeBlob(t, `{"claudeAiOauth":{"accessToken":"new","refreshToken":"r","expiresAt":2,"scopes":["user:inference"]}}`)

	origProfile := backupFetchProfileFn
	backupFetchProfileFn = func(string) backupProfile { return backupProfile{Email: "first@example.com", Tier: "Pro"} }
	defer func() { backupFetchProfileFn = origProfile }()

	if err := runBackup(); err != nil {
		t.Fatalf("runBackup: %v", err)
	}
	all, _ := store.List()
	if len(all) != 1 {
		t.Errorf("store has %d, want 1", len(all))
	}
	if all[0].Name != "first@example.com" {
		t.Errorf("Name = %q, want first@example.com", all[0].Name)
	}
}

func TestRunBackup_NoClaudeAiOauthErrors(t *testing.T) {
	setupHomeWithCcm(t)
	writePlainClaudeBlob(t, `{"claudeAiOauth":{}}`)
	if err := runBackup(); err == nil {
		t.Error("runBackup: nil err, want no-token error")
	}
}

func TestRunBackup_SyncWriteFailure_PropagatesError(t *testing.T) {
	home := setupHomeWithCcm(t)
	saveCredFor(t, "writeerr", store.OAuthTokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 1, Scopes: []string{"user:inference"}})
	installActiveBlob(t, "writeerr", store.OAuthTokens{AccessToken: "fresh", RefreshToken: "r", ExpiresAt: 99999, Scopes: []string{"user:inference"}})

	if err := os.Chmod(filepath.Join(home, ".ccm"), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(home, ".ccm"), 0700) })

	err := runBackup()
	if err == nil {
		t.Fatal("runBackup: nil err, want a write-failure error")
	}
	if !strings.Contains(err.Error(), "sync") {
		t.Errorf("err = %v, want a wrapped sync error", err)
	}
}
