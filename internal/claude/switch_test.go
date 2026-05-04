package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func setupFakeHome(t *testing.T) (claudeDir string, cleanup func()) {
	t.Helper()
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome)

	dir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create .claude dir: %v", err)
	}
	ccmDir := filepath.Join(tmpHome, ".ccm")
	if err := os.MkdirAll(ccmDir, 0700); err != nil {
		t.Fatalf("create .ccm dir: %v", err)
	}

	// Pin file backend via t.Cleanup so it interacts correctly with
	// withBackend's t.Cleanup (LIFO).
	t.Cleanup(UseFileBackendForTest())

	return dir, func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("USERPROFILE", oldUserProfile)
	}
}

func setupFakeHomeNoClaudeDir(t *testing.T) (tmpHome string, cleanup func()) {
	t.Helper()
	tmpHome = t.TempDir()
	oldHome := os.Getenv("HOME")
	oldUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome)
	t.Cleanup(UseFileBackendForTest())
	return tmpHome, func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("USERPROFILE", oldUserProfile)
	}
}

func makeCred(id string) *store.Credential {
	return &store.Credential{
		ID:   id,
		Name: "test-cred-" + id,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "access-" + id,
			RefreshToken: "refresh-" + id,
			ExpiresAt:    9999999999999,
			Scopes:       []string{"scope1", "scope2"},
		},
		CreatedAt:       "2025-01-01T00:00:00Z",
		LastRefreshedAt: "2025-01-01T00:00:00Z",
	}
}

func saveCred(t *testing.T, cred *store.Credential) {
	t.Helper()
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
}

// setActiveBlob writes a {ccmSourceId, claudeAiOauth} blob through the
// file backend so tests that previously called SetActive(id) can seed
// the active state without depending on the removed sidecar API.
func setActiveBlob(t *testing.T, id string, tokens store.OAuthTokens) {
	t.Helper()
	blob, err := encodeBlob(id, tokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := (fileBackend{}).Write(blob); err != nil {
		t.Fatal(err)
	}
}

// --- Use ---

func TestUse_ClaudeDirMissing(t *testing.T) {
	_, cleanup := setupFakeHomeNoClaudeDir(t)
	defer cleanup()

	cred := makeCred("nodir")
	if err := Use(cred); err == nil || !strings.Contains(err.Error(), "~/.claude/ does not exist") {
		t.Errorf("Use err = %v, want ~/.claude/ does not exist", err)
	}
}

func TestUse_FreshClaudeDir(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("fresh")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use: %v", err)
	}

	info, err := os.Lstat(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Error("expected regular file")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		CCMSourceID   string            `json:"ccmSourceId"`
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != cred.ID {
		t.Errorf("CCMSourceID = %q, want %q", parsed.CCMSourceID, cred.ID)
	}
	if parsed.ClaudeAiOauth.AccessToken != "access-fresh" {
		t.Errorf("AccessToken = %q", parsed.ClaudeAiOauth.AccessToken)
	}
	if id, ok := Active(); !ok || id != "fresh" {
		t.Errorf("Active() = (%q, %v), want (\"fresh\", true)", id, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "bk.credentials.json")); !os.IsNotExist(err) {
		t.Error("backup created when there was nothing to back up")
	}
}

func TestUse_BackupOnFirstActivation(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	original := []byte(`{"original": true}`)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	cred := makeCred("first")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use: %v", err)
	}

	bk, err := os.ReadFile(filepath.Join(dir, "bk.credentials.json"))
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if string(bk) != string(original) {
		t.Errorf("backup content mismatch")
	}
}

func TestUse_BackupAlreadyExists_NoClobber(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	existingBackup := []byte(`{"existing": true}`)
	if err := os.WriteFile(filepath.Join(dir, "bk.credentials.json"), existingBackup, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"now":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cred := makeCred("nobk")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use: %v", err)
	}

	bk, err := os.ReadFile(filepath.Join(dir, "bk.credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(bk) != string(existingBackup) {
		t.Errorf("backup was clobbered: got %q, want %q", bk, existingBackup)
	}
}

func TestUse_AlreadyManaged_NoBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	first := makeCred("first")
	saveCred(t, first)
	if err := Use(first); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "bk.credentials.json")); !os.IsNotExist(err) {
		t.Fatal("setup invariant: bk should not exist before switch")
	}
	second := makeCred("second")
	saveCred(t, second)
	if err := Use(second); err != nil {
		t.Fatalf("Use(second): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bk.credentials.json")); !os.IsNotExist(err) {
		t.Error("backup should not be created when switching between ccm-managed creds")
	}
	if id, _ := Active(); id != "second" {
		t.Errorf("active = %q, want second", id)
	}
}

func TestUse_RemovesPriorSymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	old := makeCred("oldsym")
	saveCred(t, old)
	if err := os.Symlink(store.CredPath(old.ID), filepath.Join(dir, ".credentials.json")); err != nil {
		t.Skip("symlink unsupported")
	}

	cred := makeCred("replacing")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use: %v", err)
	}

	info, err := os.Lstat(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("symlink not replaced with regular file")
	}
}

func TestUse_StoreFileMissingIsNotARequirement(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("inmem")
	if err := Use(cred); err != nil {
		t.Errorf("Use: %v", err)
	}
}

// --- WriteActive ---

func TestWriteActive_WritesRegularFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("wa")
	if err := WriteActive(cred); err != nil {
		t.Fatalf("WriteActive: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
		CCMSourceID   string            `json:"ccmSourceId"`
	}
	_ = json.Unmarshal(data, &parsed)
	if parsed.CCMSourceID != "wa" {
		t.Errorf("CCMSourceID = %q, want wa", parsed.CCMSourceID)
	}
	if parsed.ClaudeAiOauth.AccessToken != "access-wa" {
		t.Errorf("AccessToken = %q", parsed.ClaudeAiOauth.AccessToken)
	}
}

// --- Restore ---

func TestRestore_NoFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	// New behavior: silent no-op when nothing exists to restore.
	if err := Restore(); err != nil {
		t.Errorf("Restore() = %v, want nil (silent no-op)", err)
	}
	if _, err := os.Lstat(credentialsPath()); !os.IsNotExist(err) {
		t.Error("Restore should not have created a file")
	}
}

func TestRestore_NotManaged(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()
	content := []byte(`{"plain":true}`)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(); err != nil {
		t.Errorf("Restore: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if string(got) != string(content) {
		t.Errorf("Restore touched a non-managed file")
	}
}

func TestRestore_ManagedWithBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()
	original := []byte(`{"original": true}`)
	if err := os.WriteFile(filepath.Join(dir, "bk.credentials.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	cred := makeCred("rt")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatal(err)
	}
	if err := Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("restored content = %q, want %q", got, original)
	}
	if _, err := os.Stat(filepath.Join(dir, "bk.credentials.json")); !os.IsNotExist(err) {
		t.Error("backup should be gone after restore")
	}
	if _, ok := Active(); ok {
		t.Error("active should be cleared after Restore")
	}
}

func TestRestore_ManagedNoBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()
	cred := makeCred("nobk")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatal(err)
	}
	if err := Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
		t.Error(".credentials.json should be gone with no backup")
	}
	if _, ok := Active(); ok {
		t.Error("active should be cleared")
	}
}

// Active blob exists but the credentials file/entry was already removed
// (e.g. Claude rewrote it concurrently or the user deleted it manually).
// Restore must tolerate the missing entry as a no-op rather than error.
func TestRestore_ManagedTargetMissing_TolerateAndClear(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()
	cred := makeCred("ghost")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if err := Restore(); err != nil {
		t.Errorf("Restore: %v, want nil (missing target should be tolerated)", err)
	}
	if _, ok := Active(); ok {
		t.Error("active should be cleared even when target was missing")
	}
}

func TestUse_SwitchThenRestore_RestoresOriginal(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	original := []byte(`{"original": true}`)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), original, 0600); err != nil {
		t.Fatal(err)
	}

	a := makeCred("alpha")
	b := makeCred("beta")
	saveCred(t, a)
	saveCred(t, b)

	if err := Use(a); err != nil {
		t.Fatal(err)
	}
	if err := Use(b); err != nil {
		t.Fatal(err)
	}
	if err := Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("restored content = %q, want pre-existing original %q", got, original)
	}
	if _, ok := Active(); ok {
		t.Error("active should be cleared after Restore")
	}
}

func TestUse_WriteFails_ReturnsWrappedError(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	cred := makeCred("writefail")
	saveCred(t, cred)
	err := Use(cred)
	if err == nil {
		t.Fatal("Use: nil err, want write failure")
	}
	if !strings.Contains(err.Error(), "write credentials") {
		t.Errorf("err = %v, want wrapped 'write credentials' error", err)
	}
}

func TestRestore_RemoveFailsWithoutBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("rmfail")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	err := Restore()
	if err == nil {
		t.Fatal("Restore: nil err, want remove failure")
	}
	if !strings.Contains(err.Error(), "remove credentials") {
		t.Errorf("err = %v, want wrapped 'remove credentials' error", err)
	}
}

// With backup present + dir locked, b.Write (atomic-rename inside the
// locked dir) fails first and Restore wraps it as "restore backup".
func TestRestore_RestoreBackupWriteFails(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("renfail")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatal(err)
	}
	bk := filepath.Join(dir, "bk.credentials.json")
	if err := os.WriteFile(bk, []byte(`{"orig":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	err := Restore()
	if err == nil {
		t.Fatal("Restore: nil err, want write failure")
	}
	if !strings.Contains(err.Error(), "restore backup") {
		t.Errorf("err = %v, want wrapped 'restore backup' error", err)
	}
}

func TestUse_BackupWriteFails(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"orig":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	cred := makeCred("bkrnfail")
	saveCred(t, cred)
	err := Use(cred)
	if err == nil {
		t.Fatal("Use: nil err, want backup write failure")
	}
	if !strings.Contains(err.Error(), "backup original credentials") {
		t.Errorf("err = %v, want wrapped 'backup original credentials' error", err)
	}
}
