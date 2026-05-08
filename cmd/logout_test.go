package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/codex"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func seedLogoutCred(t *testing.T, id string) *store.Credential {
	t.Helper()
	cred := &store.Credential{
		ID:   id,
		Name: "logout-" + id,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "a",
			RefreshToken: "r",
			ExpiresAt:    9999,
			Scopes:       []string{"user:inference"},
		},
		CreatedAt:       "2026-01-01T00:00:00Z",
		LastRefreshedAt: "2026-01-01T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	return cred
}

func TestDoLogout_NotActive_JustDeletes(t *testing.T) {
	setupHomeWithCcm(t)
	cred := seedLogoutCred(t, "passive")

	if err := doLogout(cred.ID); err != nil {
		t.Fatalf("doLogout: %v", err)
	}
	if _, err := store.Load(cred.ID); err == nil {
		t.Error("cred still in store")
	}
}

func TestDoLogout_Active_RestoresFirst(t *testing.T) {
	setupHomeWithCcm(t)
	cred := seedLogoutCred(t, "active")
	if err := claude.Use(cred); err != nil {
		t.Fatal(err)
	}

	restoreCalls := 0
	orig := logoutRestoreFn
	logoutRestoreFn = func() error { restoreCalls++; return nil }
	defer func() { logoutRestoreFn = orig }()

	if err := doLogout(cred.ID); err != nil {
		t.Fatalf("doLogout: %v", err)
	}
	if restoreCalls != 1 {
		t.Errorf("Restore called %d times, want 1", restoreCalls)
	}
	if _, err := store.Load(cred.ID); err == nil {
		t.Error("cred still in store after logout")
	}
}

func TestDoLogout_Active_RestoreError_StillProceedsToDelete(t *testing.T) {
	setupHomeWithCcm(t)
	cred := seedLogoutCred(t, "rerr")
	if err := claude.Use(cred); err != nil {
		t.Fatal(err)
	}

	orig := logoutRestoreFn
	logoutRestoreFn = func() error { return errSentinel }
	defer func() { logoutRestoreFn = orig }()

	// Restore fails but delete should still happen — caller can run
	// `ccm restore` manually if they care.
	if err := doLogout(cred.ID); err != nil {
		t.Fatalf("doLogout: %v", err)
	}
	if _, err := store.Load(cred.ID); err == nil {
		t.Error("cred should still have been deleted")
	}
}

var errSentinel = &testErr{"restore failed"}

func TestLogout_Codex_RestoresAndDeletes(t *testing.T) {
	dir := setupHomeWithCcm(t)
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"original":"yes"}`)
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	cred := saveCodexCred(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "codex-logout-test")
	if err := codex.Use(cred); err != nil {
		t.Fatal(err)
	}

	// Call doLogout directly; the --force flag path is covered by the
	// RunE-level tests for claude creds; the restore+delete contract is
	// what matters here.
	if err := doLogout("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(store.CredPath("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")); !os.IsNotExist(err) {
		t.Fatal("credential file still present after logout")
	}
	got, err := os.ReadFile(filepath.Join(dir, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("codex auth.json not restored: got %q, want %q", got, original)
	}
}

func TestLogout_ActiveCodex_RestoreFailureLogsButDeletes(t *testing.T) {
	dir := setupHomeWithCcm(t)
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred := saveCodexCred(t, "cccccccc-cccc-cccc-cccc-cccccccccccd", "codex-restore-fail")
	if err := codex.Use(cred); err != nil {
		t.Fatal(err)
	}

	prev := logoutRestoreCodexFn
	logoutRestoreCodexFn = func() error { return errors.New("simulated restore failure") }
	defer func() { logoutRestoreCodexFn = prev }()

	if err := doLogout("cccccccc-cccc-cccc-cccc-cccccccccccd"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.CredPath("cccccccc-cccc-cccc-cccc-cccccccccccd")); !os.IsNotExist(err) {
		t.Fatal("credential should still be deleted on restore failure")
	}
}

func TestLogout_InactiveCodex_NoRestore(t *testing.T) {
	setupHomeWithCcm(t)
	cred := saveCodexCred(t, "dddddddd-dddd-dddd-dddd-dddddddddddd", "codex-inactive")
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	// Don't activate. Restore should NOT be called.
	called := 0
	prev := logoutRestoreCodexFn
	logoutRestoreCodexFn = func() error { called++; return nil }
	defer func() { logoutRestoreCodexFn = prev }()
	if err := doLogout("dddddddd-dddd-dddd-dddd-dddddddddddd"); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("Restore called %d times, want 0", called)
	}
}
