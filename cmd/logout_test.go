package cmd

import (
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
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
