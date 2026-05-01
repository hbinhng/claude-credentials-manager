package claude

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// writeClaudeFile writes a {claudeAiOauth: ...} regular file at
// ~/.claude/.credentials.json.
func writeClaudeFile(t *testing.T, tokens store.OAuthTokens) {
	t.Helper()
	body := map[string]any{"claudeAiOauth": tokens}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSync_NoActiveSidecar(t *testing.T) {
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

func TestSync_NoClaudeFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("nofile")
	saveCred(t, cred)
	if err := SetActive(cred.ID); err != nil {
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

func TestSync_StoreCredMissing(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := SetActive("orphan"); err != nil {
		t.Fatal(err)
	}
	writeClaudeFile(t, store.OAuthTokens{AccessToken: "t", ExpiresAt: 9999})

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
	if err := SetActive(cred.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath(), []byte("{not json"), 0600); err != nil {
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
	if err := SetActive(cred.ID); err != nil {
		t.Fatal(err)
	}
	writeClaudeFile(t, store.OAuthTokens{AccessToken: "stale", ExpiresAt: 5000})

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
	if err := SetActive(cred.ID); err != nil {
		t.Fatal(err)
	}
	writeClaudeFile(t, cred.ClaudeAiOauth)

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
	if err := SetActive(cred.ID); err != nil {
		t.Fatal(err)
	}

	fresh := store.OAuthTokens{
		AccessToken:  "new",
		RefreshToken: "newref",
		ExpiresAt:    9999,
		Scopes:       []string{"user:inference"},
	}
	writeClaudeFile(t, fresh)

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

func TestSync_CorruptActiveFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.WriteFile(activePath(), []byte("{not json"), 0600); err != nil {
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

func TestSync_StoreWriteFailure(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("perm")
	cred.ClaudeAiOauth.ExpiresAt = 1
	saveCred(t, cred)
	if err := SetActive(cred.ID); err != nil {
		t.Fatal(err)
	}
	writeClaudeFile(t, store.OAuthTokens{AccessToken: "x", ExpiresAt: 9999})

	if err := os.Chmod(store.Dir(), 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.Dir(), 0700)

	_, err := Sync()
	if err == nil {
		t.Fatal("Sync: nil, want write error")
	}
}

func TestSync_RunsMigrateFirst(t *testing.T) {
	t.Skip("requires migrate() — implemented in Task 3")

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

	info, err := os.Lstat(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected regular file, got symlink")
	}
}
