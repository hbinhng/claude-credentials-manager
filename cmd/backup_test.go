package cmd

import (
	"fmt"
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
	return home
}

func writeActiveCred(t *testing.T, id string, expiresAt int64) *store.Credential {
	t.Helper()
	cred := &store.Credential{
		ID:   id,
		Name: "named-" + id,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "stale",
			RefreshToken: "r",
			ExpiresAt:    expiresAt,
			Scopes:       []string{"user:inference"},
		},
		CreatedAt:       "2026-01-01T00:00:00Z",
		LastRefreshedAt: "2026-01-01T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	if err := claude.SetActive(id); err != nil {
		t.Fatal(err)
	}
	return cred
}

func writeClaudeJSON(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(claude.CredentialsPath(), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRunBackup_MissingClaudeFileErrors(t *testing.T) {
	setupHomeWithCcm(t)
	if err := runBackup(); err == nil {
		t.Error("runBackup: nil err, want missing-file error")
	}
}

func TestRunBackup_ActiveSidecar_SyncBranch_ClaudeNewer(t *testing.T) {
	setupHomeWithCcm(t)
	cred := writeActiveCred(t, "active-id", 1000)
	writeClaudeJSON(t, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"fresh","refreshToken":"r2","expiresAt":%d,"scopes":["user:inference"]}}`, time.Now().Add(1*time.Hour).UnixMilli()))

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

func TestRunBackup_ActiveSidecar_SyncBranch_NoChange(t *testing.T) {
	setupHomeWithCcm(t)
	cred := writeActiveCred(t, "noop", 5000)
	writeClaudeJSON(t, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"stale","refreshToken":"r","expiresAt":%d,"scopes":["user:inference"]}}`, cred.ClaudeAiOauth.ExpiresAt))

	if err := runBackup(); err != nil {
		t.Fatalf("runBackup: %v", err)
	}
	all, _ := store.List()
	if len(all) != 1 {
		t.Errorf("store has %d, want 1 (no duplicate)", len(all))
	}
}

func TestRunBackup_ActiveSidecar_StoreMissing_FallsThrough(t *testing.T) {
	setupHomeWithCcm(t)
	if err := claude.SetActive("ghost"); err != nil {
		t.Fatal(err)
	}
	writeClaudeJSON(t, `{"claudeAiOauth":{"accessToken":"new","refreshToken":"r","expiresAt":1,"scopes":["user:inference"]}}`)

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
	writeClaudeJSON(t, `{"claudeAiOauth":{"accessToken":"new","refreshToken":"r","expiresAt":2,"scopes":["user:inference"]}}`)

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
	writeClaudeJSON(t, `{"claudeAiOauth":{}}`)
	if err := runBackup(); err == nil {
		t.Error("runBackup: nil err, want no-token error")
	}
}

func TestRunBackup_SyncWriteFailure_PropagatesError(t *testing.T) {
	home := setupHomeWithCcm(t)
	writeActiveCred(t, "writeerr", 1)
	writeClaudeJSON(t, `{"claudeAiOauth":{"accessToken":"fresh","refreshToken":"r","expiresAt":99999,"scopes":["user:inference"]}}`)

	if err := os.Chmod(filepath.Join(home, ".ccm"), 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(home, ".ccm"), 0700)

	err := runBackup()
	if err == nil {
		t.Fatal("runBackup: nil err, want a write-failure error")
	}
	if !strings.Contains(err.Error(), "sync") {
		t.Errorf("err = %v, want a wrapped sync error", err)
	}
}
