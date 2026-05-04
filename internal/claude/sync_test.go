package claude

import (
	"os"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestSync_NoBackendEntry(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	changed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if changed {
		t.Error("changed = true, want false")
	}
}

func TestSync_StoreCredMissing(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	setActiveBlob(t, "orphan", store.OAuthTokens{AccessToken: "t", ExpiresAt: 9999})

	changed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if changed {
		t.Error("changed = true, want false")
	}
}

func TestSync_ClaudeUnparseable(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("badclaude")
	saveCred(t, cred)
	if err := (fileBackend{}).Write([]byte("{not json")); err != nil {
		t.Fatal(err)
	}

	changed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if changed {
		t.Error("changed = true, want false")
	}
}

func TestSync_ClaudeOlderThanStore(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("oldclaude")
	cred.ClaudeAiOauth.ExpiresAt = 9000
	saveCred(t, cred)
	setActiveBlob(t, cred.ID, store.OAuthTokens{AccessToken: "stale", ExpiresAt: 5000})

	changed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if changed {
		t.Error("changed = true, want false (store newer than claude)")
	}

	loaded, err := store.Load(cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ClaudeAiOauth.AccessToken != cred.ClaudeAiOauth.AccessToken {
		t.Errorf("store accessToken changed unexpectedly")
	}
}

func TestSync_ClaudeEqualToStore(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("equal")
	cred.ClaudeAiOauth.ExpiresAt = 7000
	saveCred(t, cred)
	setActiveBlob(t, cred.ID, cred.ClaudeAiOauth)

	changed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if changed {
		t.Error("changed = true, want false (equal)")
	}
}

func TestSync_ClaudeNewer_StoreUpdated(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("newer")
	cred.ClaudeAiOauth.ExpiresAt = 1000
	cred.ClaudeAiOauth.AccessToken = "old"
	saveCred(t, cred)

	fresh := store.OAuthTokens{
		AccessToken:  "new",
		RefreshToken: "newref",
		ExpiresAt:    9999,
		Scopes:       []string{"user:inference"},
	}
	setActiveBlob(t, cred.ID, fresh)

	changed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true (claude newer)")
	}

	loaded, err := store.Load(cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ClaudeAiOauth.AccessToken != "new" {
		t.Errorf("AccessToken = %q, want new", loaded.ClaudeAiOauth.AccessToken)
	}
	if loaded.ClaudeAiOauth.RefreshToken != "newref" {
		t.Errorf("RefreshToken = %q, want newref", loaded.ClaudeAiOauth.RefreshToken)
	}
	if loaded.ClaudeAiOauth.ExpiresAt != 9999 {
		t.Errorf("ExpiresAt = %d, want 9999", loaded.ClaudeAiOauth.ExpiresAt)
	}
	if loaded.Name != cred.Name {
		t.Errorf("Name = %q, want %q (must be preserved)", loaded.Name, cred.Name)
	}
}

func TestSync_StoreWriteFailure(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("perm")
	cred.ClaudeAiOauth.ExpiresAt = 1
	saveCred(t, cred)
	setActiveBlob(t, cred.ID, store.OAuthTokens{AccessToken: "x", ExpiresAt: 9999})

	ccmDir := store.Dir()
	if err := os.Chmod(ccmDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ccmDir, 0700) })

	_, err := Sync()
	if err == nil {
		t.Fatal("Sync: nil, want write error")
	}
}

func TestSync_RunsMigrateFirst(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("symlinked")
	cred.ClaudeAiOauth.ExpiresAt = 5000
	saveCred(t, cred)

	if err := os.Symlink(store.CredPath(cred.ID), credentialsPath()); err != nil {
		t.Skip("symlink unsupported on this platform")
	}

	changed, err := Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	_ = changed

	id, ok := Active()
	if !ok || id != cred.ID {
		t.Errorf("Active() after migrate = (%q, %v), want (%q, true)", id, ok, cred.ID)
	}
}
