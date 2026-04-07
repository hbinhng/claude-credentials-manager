package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// setupFakeHome creates a temp directory, sets HOME to it, creates ~/.claude/ and ~/.ccm/,
// and returns the path to the .claude directory. The caller should defer
// restoring HOME via the returned cleanup function.
func setupFakeHome(t *testing.T) (claudeDir string, cleanup func()) {
	t.Helper()
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome) // Windows compat

	dir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create .claude dir: %v", err)
	}

	ccmDir := filepath.Join(tmpHome, ".ccm")
	if err := os.MkdirAll(ccmDir, 0700); err != nil {
		t.Fatalf("create .ccm dir: %v", err)
	}

	return dir, func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("USERPROFILE", oldUserProfile)
	}
}

// setupFakeHomeNoClaudeDir creates a temp directory and sets HOME to it,
// but does NOT create the .claude directory.
func setupFakeHomeNoClaudeDir(t *testing.T) (tmpHome string, cleanup func()) {
	t.Helper()
	tmpHome = t.TempDir()
	oldHome := os.Getenv("HOME")
	oldUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tmpHome)
	os.Setenv("USERPROFILE", tmpHome)
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

// saveCred writes a credential to the fake ~/.ccm/ store.
func saveCred(t *testing.T, cred *store.Credential) {
	t.Helper()
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
}

// --- ActiveID tests ---

func TestActiveID_NoCredentialsFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	got := ActiveID()
	if got != "" {
		t.Errorf("ActiveID() = %q, want %q", got, "")
	}
}

func TestActiveID_RegularFileNotSymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Write a regular file (not a symlink)
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(creds, []byte(`{"claudeAiOauth":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "" {
		t.Errorf("ActiveID() = %q, want %q", got, "")
	}
}

func TestActiveID_SymlinkToStoreFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("my-cred-id")
	saveCred(t, cred)

	// Create symlink to store file
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(store.CredPath("my-cred-id"), creds); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "my-cred-id" {
		t.Errorf("ActiveID() = %q, want %q", got, "my-cred-id")
	}
}

func TestActiveID_SymlinkToStoreButFileMissing(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create symlink to a store file that doesn't exist
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(store.CredPath("nonexistent"), creds); err != nil {
		t.Fatal(err)
	}

	// Should still return the ID (file existence is not checked by ActiveID)
	got := ActiveID()
	if got != "nonexistent" {
		t.Errorf("ActiveID() = %q, want %q", got, "nonexistent")
	}
}

func TestActiveID_SymlinkToSomethingElse(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create a different target file
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Symlink to something outside the store
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("other.json", creds); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "" {
		t.Errorf("ActiveID() = %q, want %q", got, "")
	}
}

// --- Old format backward compat ---

func TestActiveID_OldFormatSymlinkToCcm(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Old format: ccm.credentials.json with ccmSourceId
	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, _ := json.Marshal(map[string]any{
		"ccmSourceId":   "old-format-id",
		"claudeAiOauth": map[string]any{"accessToken": "tok"},
	})
	if err := os.WriteFile(ccm, data, 0600); err != nil {
		t.Fatal(err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "old-format-id" {
		t.Errorf("ActiveID() = %q, want %q", got, "old-format-id")
	}
}

// --- IsActive tests ---

func TestIsActive_MatchingID(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("abc-123")
	saveCred(t, cred)

	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(store.CredPath("abc-123"), creds); err != nil {
		t.Fatal(err)
	}

	if !IsActive("abc-123") {
		t.Error("IsActive(\"abc-123\") = false, want true")
	}
}

func TestIsActive_NonMatchingID(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("abc-123")
	saveCred(t, cred)

	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(store.CredPath("abc-123"), creds); err != nil {
		t.Fatal(err)
	}

	if IsActive("different-id") {
		t.Error("IsActive(\"different-id\") = true, want false")
	}
}

func TestIsActive_NoSetup(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if IsActive("anything") {
		t.Error("IsActive(\"anything\") = true, want false (no credentials at all)")
	}
}

// --- WriteActive tests (no-op on Unix) ---

func TestWriteActive_NoOpOnUnix(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if !useSymlinks() {
		t.Skip("test only applies to symlink systems")
	}

	cred := makeCred("write-test")
	if err := WriteActive(cred); err != nil {
		t.Fatalf("WriteActive() error: %v", err)
	}

	// Should NOT create ccm.credentials.json on Unix
	ccm := filepath.Join(claudeDir(), "ccm.credentials.json")
	if _, err := os.Stat(ccm); !os.IsNotExist(err) {
		t.Error("WriteActive should not create ccm.credentials.json on Unix")
	}
}

// --- Use tests ---

func TestUse_ClaudeDirDoesNotExist(t *testing.T) {
	_, cleanup := setupFakeHomeNoClaudeDir(t)
	defer cleanup()

	cred := makeCred("no-dir")
	err := Use(cred)
	if err == nil {
		t.Fatal("Use() expected error when ~/.claude/ does not exist")
	}
	if got := err.Error(); got != "~/.claude/ does not exist. Has Claude Code been run before?" {
		t.Errorf("error = %q, want mention of ~/.claude/", got)
	}
}

func TestUse_RegularFileNoBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create a regular .credentials.json
	creds := filepath.Join(dir, ".credentials.json")
	originalContent := []byte(`{"original": true}`)
	if err := os.WriteFile(creds, originalContent, 0600); err != nil {
		t.Fatal(err)
	}

	cred := makeCred("use-test-1")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	// Check backup was created
	backup := filepath.Join(dir, "bk.credentials.json")
	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup not created: %v", err)
	}
	if string(backupData) != string(originalContent) {
		t.Errorf("backup content = %q, want %q", backupData, originalContent)
	}

	// Check symlink exists and points to store file
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != store.CredPath("use-test-1") {
		t.Errorf("symlink target = %q, want %q", target, store.CredPath("use-test-1"))
	}

	// Verify we can read the credential through the symlink
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ClaudeAiOauth.AccessToken != "access-use-test-1" {
		t.Errorf("accessToken = %q, want %q", parsed.ClaudeAiOauth.AccessToken, "access-use-test-1")
	}
}

func TestUse_RegularFileBackupAlreadyExists(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create an existing backup
	backup := filepath.Join(dir, "bk.credentials.json")
	existingBackup := []byte(`{"existingBackup": true}`)
	if err := os.WriteFile(backup, existingBackup, 0600); err != nil {
		t.Fatal(err)
	}

	// Create a regular .credentials.json with different content
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(creds, []byte(`{"current": true}`), 0600); err != nil {
		t.Fatal(err)
	}

	cred := makeCred("use-test-2")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	// Backup should NOT have been overwritten
	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(backupData) != string(existingBackup) {
		t.Errorf("backup was overwritten: got %q, want %q", backupData, existingBackup)
	}

	// .credentials.json should now be a symlink to store file
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != store.CredPath("use-test-2") {
		t.Errorf("symlink target = %q, want %q", target, store.CredPath("use-test-2"))
	}
}

func TestUse_AlreadySymlink_SwitchAccounts(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Set up initial state: symlink to old credential
	oldCred := makeCred("old-cred")
	saveCred(t, oldCred)
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(store.CredPath("old-cred"), creds); err != nil {
		t.Fatal(err)
	}

	// Now switch to a new credential
	newCred := makeCred("new-cred")
	saveCred(t, newCred)
	if err := Use(newCred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	// Symlink should point to new store file
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != store.CredPath("new-cred") {
		t.Errorf("symlink target = %q, want %q", target, store.CredPath("new-cred"))
	}
}

func TestUse_NoCredentialsFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	// .credentials.json doesn't exist at all; .claude/ does
	cred := makeCred("fresh")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	dir := claudeDir()

	// Symlink should be created pointing to store file
	creds := filepath.Join(dir, ".credentials.json")
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != store.CredPath("fresh") {
		t.Errorf("symlink target = %q, want %q", target, store.CredPath("fresh"))
	}

	// No backup should exist
	backup := filepath.Join(dir, "bk.credentials.json")
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup should not exist when there was no original credentials file")
	}

	// Verify the credential data is readable through the symlink
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ClaudeAiOauth json.RawMessage `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ClaudeAiOauth == nil {
		t.Error("claudeAiOauth should not be nil")
	}
}

func TestUse_SymlinkIsAbsolute(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("abs-check")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatal(err)
	}

	// Must be absolute, pointing to the store
	if !filepath.IsAbs(target) {
		t.Errorf("symlink target %q is relative, want absolute", target)
	}
	if !strings.HasPrefix(target, store.Dir()+string(filepath.Separator)) {
		t.Errorf("symlink target %q should be inside store dir %q", target, store.Dir())
	}
}

func TestUse_CleansUpOldCcmFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Simulate old format: ccm.credentials.json exists
	ccm := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(ccm, []byte(`{"old": true}`), 0600); err != nil {
		t.Fatal(err)
	}

	cred := makeCred("migrate")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	// Old ccm.credentials.json should be cleaned up
	if _, err := os.Stat(ccm); !os.IsNotExist(err) {
		t.Error("ccm.credentials.json should have been removed after migration")
	}
}

func TestUse_StoreFileMissing(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("missing-store")
	// Deliberately do NOT save to store
	err := Use(cred)
	if err == nil {
		t.Fatal("Use() expected error when store file doesn't exist")
	}
	if !strings.Contains(err.Error(), "credential file not found") {
		t.Errorf("error = %q, want mention of credential file not found", err.Error())
	}
}

func TestUse_ReadsThroughSymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("oauth-check")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	// Read through the symlink and verify oauth data
	creds := filepath.Join(dir, ".credentials.json")
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed.ClaudeAiOauth.AccessToken != "access-oauth-check" {
		t.Errorf("accessToken = %q, want %q", parsed.ClaudeAiOauth.AccessToken, "access-oauth-check")
	}
	if parsed.ClaudeAiOauth.RefreshToken != "refresh-oauth-check" {
		t.Errorf("refreshToken = %q, want %q", parsed.ClaudeAiOauth.RefreshToken, "refresh-oauth-check")
	}
}

// --- Restore tests ---

func TestRestore_SymlinkWithBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Set up: backup exists, symlink to store exists
	backup := filepath.Join(dir, "bk.credentials.json")
	originalContent := []byte(`{"original": true}`)
	if err := os.WriteFile(backup, originalContent, 0600); err != nil {
		t.Fatal(err)
	}

	cred := makeCred("restore-test")
	saveCred(t, cred)
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(store.CredPath("restore-test"), creds); err != nil {
		t.Fatal(err)
	}

	if err := Restore(); err != nil {
		t.Fatalf("Restore() error: %v", err)
	}

	// Symlink should be gone, replaced by the backup content
	info, err := os.Lstat(creds)
	if err != nil {
		t.Fatalf(".credentials.json missing after restore: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error(".credentials.json is still a symlink after restore")
	}

	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(originalContent) {
		t.Errorf("restored content = %q, want %q", data, originalContent)
	}

	// Backup should be gone (it was renamed)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup should have been removed after restore")
	}
}

func TestRestore_SymlinkNoBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("no-backup")
	saveCred(t, cred)
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(store.CredPath("no-backup"), creds); err != nil {
		t.Fatal(err)
	}

	if err := Restore(); err != nil {
		t.Fatalf("Restore() error: %v", err)
	}

	// Symlink should be removed
	if _, err := os.Lstat(creds); !os.IsNotExist(err) {
		t.Error(".credentials.json should not exist after restore with no backup")
	}
}

func TestRestore_NotASymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// .credentials.json is a regular file (not managed by CCM)
	creds := filepath.Join(dir, ".credentials.json")
	content := []byte(`{"regular": true}`)
	if err := os.WriteFile(creds, content, 0600); err != nil {
		t.Fatal(err)
	}

	err := Restore()
	if err != nil {
		t.Fatalf("Restore() should not error for a regular file, got: %v", err)
	}

	// The regular file should be untouched
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Errorf("file content changed: got %q, want %q", data, content)
	}
}

func TestRestore_CredentialsFileDoesNotExist(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	err := Restore()
	if err == nil {
		t.Fatal("Restore() expected error when .credentials.json does not exist")
	}
}

func TestRestore_OldFormatSymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Old format: symlink to ccm.credentials.json
	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, _ := json.Marshal(map[string]any{
		"ccmSourceId":   "old-id",
		"claudeAiOauth": map[string]any{},
	})
	os.WriteFile(ccm, data, 0600)

	creds := filepath.Join(dir, ".credentials.json")
	os.Symlink("ccm.credentials.json", creds)

	backup := filepath.Join(dir, "bk.credentials.json")
	os.WriteFile(backup, []byte(`{"original": true}`), 0600)

	if err := Restore(); err != nil {
		t.Fatalf("Restore() error: %v", err)
	}

	// Should have restored backup
	restoredData, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredData) != `{"original": true}` {
		t.Errorf("restored content = %q, want original", restoredData)
	}

	// Old ccm.credentials.json should be cleaned up
	if _, err := os.Stat(ccm); !os.IsNotExist(err) {
		t.Error("ccm.credentials.json should be cleaned up after restore")
	}
}

// --- Integration: Use then ActiveID ---

func TestUse_ThenActiveID(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("integration-test")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	got := ActiveID()
	if got != "integration-test" {
		t.Errorf("ActiveID() after Use() = %q, want %q", got, "integration-test")
	}
}

func TestUse_ThenRestore_ThenActiveID(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("restore-flow")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	if err := Restore(); err != nil {
		t.Fatalf("Restore() error: %v", err)
	}

	got := ActiveID()
	if got != "" {
		t.Errorf("ActiveID() after Restore() = %q, want %q", got, "")
	}
}

func TestUse_SwitchBetweenCredentials(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred1 := makeCred("cred-alpha")
	saveCred(t, cred1)
	if err := Use(cred1); err != nil {
		t.Fatalf("Use(alpha) error: %v", err)
	}
	if got := ActiveID(); got != "cred-alpha" {
		t.Errorf("ActiveID() = %q, want %q", got, "cred-alpha")
	}

	cred2 := makeCred("cred-beta")
	saveCred(t, cred2)
	if err := Use(cred2); err != nil {
		t.Fatalf("Use(beta) error: %v", err)
	}
	if got := ActiveID(); got != "cred-beta" {
		t.Errorf("ActiveID() = %q, want %q", got, "cred-beta")
	}

	if IsActive("cred-alpha") {
		t.Error("IsActive(\"cred-alpha\") should be false after switching to beta")
	}
	if !IsActive("cred-beta") {
		t.Error("IsActive(\"cred-beta\") should be true")
	}
}

// --- Refresh reflects immediately through symlink ---

func TestRefresh_UpdatesVisibleThroughSymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("refresh-test")
	saveCred(t, cred)
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	// Simulate a refresh: update the store file
	cred.ClaudeAiOauth.AccessToken = "refreshed-token"
	saveCred(t, cred)

	// Read through the symlink — should see the updated token immediately
	creds := filepath.Join(dir, ".credentials.json")
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ClaudeAiOauth.AccessToken != "refreshed-token" {
		t.Errorf("accessToken = %q, want %q (should reflect store update)", parsed.ClaudeAiOauth.AccessToken, "refreshed-token")
	}
}

// --- isCCMManaged tests ---

func TestIsCCMManaged_SymlinkToStore(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("managed-test")
	saveCred(t, cred)

	creds := filepath.Join(dir, ".credentials.json")
	os.Symlink(store.CredPath("managed-test"), creds)

	if !isCCMManaged(creds) {
		t.Error("isCCMManaged should return true for symlink to store file")
	}
}

func TestIsCCMManaged_OldFormatSymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	ccm := filepath.Join(dir, "ccm.credentials.json")
	os.WriteFile(ccm, []byte(`{"ccmSourceId":"x"}`), 0600)

	creds := filepath.Join(dir, ".credentials.json")
	os.Symlink("ccm.credentials.json", creds)

	if !isCCMManaged(creds) {
		t.Error("isCCMManaged should return true for old format symlink to ccm.credentials.json")
	}
}

func TestIsCCMManaged_RegularFileWithoutMarker(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	creds := filepath.Join(dir, ".credentials.json")
	os.WriteFile(creds, []byte(`{"claudeAiOauth":{}}`), 0600)

	if isCCMManaged(creds) {
		t.Error("isCCMManaged should return false for regular file without ccmSourceId")
	}
}

func TestIsCCMManaged_NonExistent(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	creds := filepath.Join(dir, ".credentials.json")
	if isCCMManaged(creds) {
		t.Error("isCCMManaged should return false for non-existent file")
	}
}

// --- writeCredentialsFile (Windows copy path) ---

func TestWriteCredentialsFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("copy-test")
	if err := writeCredentialsFile(cred); err != nil {
		t.Fatalf("writeCredentialsFile() error: %v", err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatalf("read .credentials.json: %v", err)
	}

	var parsed struct {
		CCMSourceID   string          `json:"ccmSourceId"`
		ClaudeAiOauth json.RawMessage `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.CCMSourceID != "copy-test" {
		t.Errorf("ccmSourceId = %q, want %q", parsed.CCMSourceID, "copy-test")
	}
	if parsed.ClaudeAiOauth == nil {
		t.Error("claudeAiOauth should not be nil")
	}
}

func TestWriteCredentialsFile_Permissions(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("perm-copy")
	if err := writeCredentialsFile(cred); err != nil {
		t.Fatalf("writeCredentialsFile() error: %v", err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	info, err := os.Stat(creds)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("permissions = %o, want 0600", perm)
	}
}
