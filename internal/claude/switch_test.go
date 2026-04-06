package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hbinhng/ccm/internal/store"
)

// setupFakeHome creates a temp directory, sets HOME to it, creates ~/.claude/,
// and returns the path to the .claude directory. The caller should defer
// restoring HOME via the returned cleanup function.
func setupFakeHome(t *testing.T) (claudeDir string, cleanup func()) {
	t.Helper()
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)

	dir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("create .claude dir: %v", err)
	}

	return dir, func() {
		os.Setenv("HOME", oldHome)
	}
}

// setupFakeHomeNoClaudeDir creates a temp directory and sets HOME to it,
// but does NOT create the .claude directory.
func setupFakeHomeNoClaudeDir(t *testing.T) (tmpHome string, cleanup func()) {
	t.Helper()
	tmpHome = t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	return tmpHome, func() {
		os.Setenv("HOME", oldHome)
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

func TestActiveID_SymlinkToCcmButCcmMissing(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create symlink pointing to ccm.credentials.json, but don't create that file
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "" {
		t.Errorf("ActiveID() = %q, want %q", got, "")
	}
}

func TestActiveID_SymlinkToCcmWithValidSourceId(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create ccm.credentials.json with a valid ccmSourceId
	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, _ := json.Marshal(map[string]any{
		"ccmSourceId": "my-cred-id",
		"claudeAiOauth": map[string]any{
			"accessToken": "tok",
		},
	})
	if err := os.WriteFile(ccm, data, 0600); err != nil {
		t.Fatal(err)
	}

	// Create symlink
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "my-cred-id" {
		t.Errorf("ActiveID() = %q, want %q", got, "my-cred-id")
	}
}

func TestActiveID_SymlinkToSomethingElse(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create a different target file
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(`{"ccmSourceId":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Symlink to something other than ccm.credentials.json
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("other.json", creds); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "" {
		t.Errorf("ActiveID() = %q, want %q", got, "")
	}
}

func TestActiveID_CcmExistsButNoCcmSourceId(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Create ccm.credentials.json without ccmSourceId
	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "tok",
		},
	})
	if err := os.WriteFile(ccm, data, 0600); err != nil {
		t.Fatal(err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
		t.Fatal(err)
	}

	got := ActiveID()
	if got != "" {
		t.Errorf("ActiveID() = %q, want %q (no ccmSourceId in file)", got, "")
	}
}

// --- IsActive tests ---

func TestIsActive_MatchingID(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, _ := json.Marshal(map[string]any{
		"ccmSourceId":   "abc-123",
		"claudeAiOauth": map[string]any{},
	})
	if err := os.WriteFile(ccm, data, 0600); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
		t.Fatal(err)
	}

	if !IsActive("abc-123") {
		t.Error("IsActive(\"abc-123\") = false, want true")
	}
}

func TestIsActive_NonMatchingID(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, _ := json.Marshal(map[string]any{
		"ccmSourceId":   "abc-123",
		"claudeAiOauth": map[string]any{},
	})
	if err := os.WriteFile(ccm, data, 0600); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
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

// --- WriteActive tests ---

func TestWriteActive_WritesCorrectJSON(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("write-test")
	if err := WriteActive(cred); err != nil {
		t.Fatalf("WriteActive() error: %v", err)
	}

	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, err := os.ReadFile(ccm)
	if err != nil {
		t.Fatalf("read ccm.credentials.json: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check ccmSourceId
	var sourceID string
	if err := json.Unmarshal(parsed["ccmSourceId"], &sourceID); err != nil {
		t.Fatalf("unmarshal ccmSourceId: %v", err)
	}
	if sourceID != "write-test" {
		t.Errorf("ccmSourceId = %q, want %q", sourceID, "write-test")
	}

	// Check claudeAiOauth
	var oauth store.OAuthTokens
	if err := json.Unmarshal(parsed["claudeAiOauth"], &oauth); err != nil {
		t.Fatalf("unmarshal claudeAiOauth: %v", err)
	}
	if oauth.AccessToken != "access-write-test" {
		t.Errorf("accessToken = %q, want %q", oauth.AccessToken, "access-write-test")
	}
	if oauth.RefreshToken != "refresh-write-test" {
		t.Errorf("refreshToken = %q, want %q", oauth.RefreshToken, "refresh-write-test")
	}
	if oauth.ExpiresAt != 9999999999999 {
		t.Errorf("expiresAt = %d, want %d", oauth.ExpiresAt, 9999999999999)
	}
	if len(oauth.Scopes) != 2 || oauth.Scopes[0] != "scope1" || oauth.Scopes[1] != "scope2" {
		t.Errorf("scopes = %v, want [scope1 scope2]", oauth.Scopes)
	}
}

func TestWriteActive_FilePermissions(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("perm-test")
	if err := WriteActive(cred); err != nil {
		t.Fatalf("WriteActive() error: %v", err)
	}

	ccm := filepath.Join(dir, "ccm.credentials.json")
	info, err := os.Stat(ccm)
	if err != nil {
		t.Fatalf("stat ccm.credentials.json: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want %o", perm, 0600)
	}
}

func TestWriteActive_OverwritesExistingFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Write first credential
	cred1 := makeCred("first")
	if err := WriteActive(cred1); err != nil {
		t.Fatalf("WriteActive(first) error: %v", err)
	}

	// Write second credential over the first
	cred2 := makeCred("second")
	if err := WriteActive(cred2); err != nil {
		t.Fatalf("WriteActive(second) error: %v", err)
	}

	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, err := os.ReadFile(ccm)
	if err != nil {
		t.Fatalf("read ccm.credentials.json: %v", err)
	}

	var parsed struct {
		CCMSourceID string `json:"ccmSourceId"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != "second" {
		t.Errorf("ccmSourceId = %q, want %q", parsed.CCMSourceID, "second")
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

	// Check symlink exists and points to ccm.credentials.json
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "ccm.credentials.json" {
		t.Errorf("symlink target = %q, want %q", target, "ccm.credentials.json")
	}

	// Check ccm.credentials.json was written
	ccm := filepath.Join(dir, "ccm.credentials.json")
	ccmData, err := os.ReadFile(ccm)
	if err != nil {
		t.Fatalf("ccm.credentials.json not created: %v", err)
	}
	var parsed struct {
		CCMSourceID string `json:"ccmSourceId"`
	}
	if err := json.Unmarshal(ccmData, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != "use-test-1" {
		t.Errorf("ccmSourceId = %q, want %q", parsed.CCMSourceID, "use-test-1")
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

	// .credentials.json should now be a symlink
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "ccm.credentials.json" {
		t.Errorf("symlink target = %q, want %q", target, "ccm.credentials.json")
	}

	// ccm.credentials.json should have the new credential
	ccm := filepath.Join(dir, "ccm.credentials.json")
	ccmData, err := os.ReadFile(ccm)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		CCMSourceID string `json:"ccmSourceId"`
	}
	if err := json.Unmarshal(ccmData, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != "use-test-2" {
		t.Errorf("ccmSourceId = %q, want %q", parsed.CCMSourceID, "use-test-2")
	}
}

func TestUse_AlreadySymlink_SwitchAccounts(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Set up initial state: symlink already exists from a previous Use()
	ccm := filepath.Join(dir, "ccm.credentials.json")
	oldData, _ := json.Marshal(map[string]any{
		"ccmSourceId":   "old-cred",
		"claudeAiOauth": map[string]any{"accessToken": "old-token"},
	})
	if err := os.WriteFile(ccm, oldData, 0600); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
		t.Fatal(err)
	}

	// Now switch to a new credential
	newCred := makeCred("new-cred")
	if err := Use(newCred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	// Symlink should exist and point to ccm.credentials.json
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "ccm.credentials.json" {
		t.Errorf("symlink target = %q, want %q", target, "ccm.credentials.json")
	}

	// ccm.credentials.json should have the new credential
	ccmData, err := os.ReadFile(ccm)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		CCMSourceID string `json:"ccmSourceId"`
	}
	if err := json.Unmarshal(ccmData, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != "new-cred" {
		t.Errorf("ccmSourceId = %q, want %q", parsed.CCMSourceID, "new-cred")
	}
}

func TestUse_NoCredentialsFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	// .credentials.json doesn't exist at all; .claude/ does
	cred := makeCred("fresh")
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	dir := claudeDir()

	// Symlink should be created
	creds := filepath.Join(dir, ".credentials.json")
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "ccm.credentials.json" {
		t.Errorf("symlink target = %q, want %q", target, "ccm.credentials.json")
	}

	// No backup should exist
	backup := filepath.Join(dir, "bk.credentials.json")
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup should not exist when there was no original credentials file")
	}

	// ccm.credentials.json should contain correct data
	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, err := os.ReadFile(ccm)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		CCMSourceID   string          `json:"ccmSourceId"`
		ClaudeAiOauth json.RawMessage `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != "fresh" {
		t.Errorf("ccmSourceId = %q, want %q", parsed.CCMSourceID, "fresh")
	}
	if parsed.ClaudeAiOauth == nil {
		t.Error("claudeAiOauth should not be nil")
	}
}

func TestUse_SymlinkTargetIsRelative(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("relative-check")
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	target, err := os.Readlink(creds)
	if err != nil {
		t.Fatal(err)
	}

	// Must be relative, not absolute
	if filepath.IsAbs(target) {
		t.Errorf("symlink target %q is absolute, want relative", target)
	}
	if target != "ccm.credentials.json" {
		t.Errorf("symlink target = %q, want exactly %q", target, "ccm.credentials.json")
	}
}

func TestUse_CcmCredentialsContainsOAuthData(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("oauth-check")
	if err := Use(cred); err != nil {
		t.Fatalf("Use() error: %v", err)
	}

	ccm := filepath.Join(dir, "ccm.credentials.json")
	data, err := os.ReadFile(ccm)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	oauth := parsed.ClaudeAiOauth
	if oauth.AccessToken != "access-oauth-check" {
		t.Errorf("accessToken = %q, want %q", oauth.AccessToken, "access-oauth-check")
	}
	if oauth.RefreshToken != "refresh-oauth-check" {
		t.Errorf("refreshToken = %q, want %q", oauth.RefreshToken, "refresh-oauth-check")
	}
}

// --- Restore tests ---

func TestRestore_SymlinkWithBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Set up: backup exists, symlink exists, ccm exists
	backup := filepath.Join(dir, "bk.credentials.json")
	originalContent := []byte(`{"original": true}`)
	if err := os.WriteFile(backup, originalContent, 0600); err != nil {
		t.Fatal(err)
	}

	ccm := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(ccm, []byte(`{"ccmSourceId":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
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

	// ccm.credentials.json should be cleaned up
	if _, err := os.Stat(ccm); !os.IsNotExist(err) {
		t.Error("ccm.credentials.json should have been removed after restore")
	}
}

func TestRestore_SymlinkNoBackup(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Set up: no backup, symlink exists, ccm exists
	ccm := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(ccm, []byte(`{"ccmSourceId":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}

	creds := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink("ccm.credentials.json", creds); err != nil {
		t.Fatal(err)
	}

	if err := Restore(); err != nil {
		t.Fatalf("Restore() error: %v", err)
	}

	// Symlink should be removed
	if _, err := os.Lstat(creds); !os.IsNotExist(err) {
		t.Error(".credentials.json should not exist after restore with no backup")
	}

	// ccm.credentials.json should be cleaned up
	if _, err := os.Stat(ccm); !os.IsNotExist(err) {
		t.Error("ccm.credentials.json should have been removed after restore")
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

// --- Integration: Use then ActiveID ---

func TestUse_ThenActiveID(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("integration-test")
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
	if err := Use(cred1); err != nil {
		t.Fatalf("Use(alpha) error: %v", err)
	}
	if got := ActiveID(); got != "cred-alpha" {
		t.Errorf("ActiveID() = %q, want %q", got, "cred-alpha")
	}

	cred2 := makeCred("cred-beta")
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
