//go:build !windows

package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// setupFakeHomeInternal mirrors the external helper but stays in the
// internal package so we can call unexported functions.
func setupFakeHomeInternal(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	for _, sub := range []string{".ccm", ".codex"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestDecodeBlob_InvalidJSON covers the json.Unmarshal error path.
func TestDecodeBlob_InvalidJSON(t *testing.T) {
	_, err := decodeBlob([]byte("not-json"))
	if err == nil || !strings.Contains(err.Error(), "codex: parse blob") {
		t.Fatalf("want parse error; got %v", err)
	}
}

// TestDecodeBlob_NonCodexProvider covers provider != "codex" → returns "".
func TestDecodeBlob_NonCodexProvider(t *testing.T) {
	blob := []byte(`{"id":"abc","provider":"claude"}`)
	id, err := decodeBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("want empty id for non-codex provider; got %q", id)
	}
}

// TestDecodeBlob_CodexProvider covers explicit provider=="codex" → returns id.
func TestDecodeBlob_CodexProvider(t *testing.T) {
	blob := []byte(`{"id":"xyz","provider":"codex"}`)
	id, err := decodeBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if id != "xyz" {
		t.Fatalf("want 'xyz'; got %q", id)
	}
}

// TestDecodeBlob_CCMSourceID covers the Windows wrapper-copy path
// (ccmSourceId key with no id key).
func TestDecodeBlob_CCMSourceID(t *testing.T) {
	blob := []byte(`{"ccmSourceId":"win-id"}`)
	id, err := decodeBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if id != "win-id" {
		t.Fatalf("want 'win-id'; got %q", id)
	}
}

// TestEncodeBlob_NonCodex covers the non-codex guard.
func TestEncodeBlob_NonCodex(t *testing.T) {
	setupFakeHomeInternal(t)
	cred := &store.Credential{ID: "x", Provider: "claude"}
	_, err := encodeBlob(cred)
	if err == nil || !strings.Contains(err.Error(), "non-codex") {
		t.Fatalf("want non-codex error; got %v", err)
	}
}

// TestFileBackend_Read_IOError covers a real IO error from Read (not NotExist).
func TestFileBackend_Read_IOError(t *testing.T) {
	dir := setupFakeHomeInternal(t)
	// Create auth.json as a directory so ReadFile returns an error that
	// is NOT os.ErrNotExist.
	p := filepath.Join(dir, ".codex", "auth.json")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	_, ok, err := (fileBackend{}).Read()
	if err == nil {
		t.Fatal("want IO error; got nil")
	}
	if ok {
		t.Fatal("want ok=false on error")
	}
}

// TestFileBackend_Remove_NonExistError covers the non-NotExist error path.
func TestFileBackend_Remove_NonExistError(t *testing.T) {
	dir := setupFakeHomeInternal(t)
	// Create auth.json as a non-empty directory so os.Remove returns
	// ENOTEMPTY (a real error, not ErrNotExist).
	p := filepath.Join(dir, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Join(p, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := (fileBackend{}).Remove()
	if err == nil {
		t.Fatal("want error removing directory; got nil")
	}
	if !strings.Contains(err.Error(), "codex: remove auth.json") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFileBackend_Write_EmptyID covers the empty-id guard in Write.
func TestFileBackend_Write_EmptyID(t *testing.T) {
	setupFakeHomeInternal(t)
	// A blob with no id or ccmSourceId → decodeBlob returns ""
	err := (fileBackend{}).Write([]byte(`{"something":"else"}`))
	if err == nil || !strings.Contains(err.Error(), "cannot determine source id") {
		t.Fatalf("want empty-id error; got %v", err)
	}
}

// TestFileBackend_Write_InvalidBlob covers the decodeBlob error path in Write.
func TestFileBackend_Write_InvalidBlob(t *testing.T) {
	setupFakeHomeInternal(t)
	err := (fileBackend{}).Write([]byte("not-json"))
	if err == nil || !strings.Contains(err.Error(), "codex: parse blob") {
		t.Fatalf("want parse error; got %v", err)
	}
}

// TestFileBackend_Write_SymlinkError covers the symlink failure path.
func TestFileBackend_Write_SymlinkError(t *testing.T) {
	dir := setupFakeHomeInternal(t)
	codexD := filepath.Join(dir, ".codex")
	// Write a dummy auth.json first (will be removed by Write)
	if err := os.WriteFile(filepath.Join(codexD, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make .codex read-only so os.Symlink inside it fails.
	if err := os.Chmod(codexD, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(codexD, 0o755) })

	// blob with a valid id so decodeBlob succeeds past the id check
	blob := []byte(`{"id":"someid","provider":"codex"}`)
	err := (fileBackend{}).Write(blob)
	if err == nil || !strings.Contains(err.Error(), "codex: symlink auth.json") {
		t.Fatalf("want symlink error; got %v", err)
	}
}

// TestUse_NonCodexCred covers the encodeBlob error branch in Use.
func TestUse_NonCodexCred(t *testing.T) {
	setupFakeHomeInternal(t)
	// Provider "claude" causes encodeBlob to return an error.
	cred := &store.Credential{ID: "x", Provider: "claude"}
	err := Use(cred)
	if err == nil || !strings.Contains(err.Error(), "non-codex") {
		t.Fatalf("want non-codex error; got %v", err)
	}
}

// TestUse_BackupWriteError covers WriteFile(backupPath) failure in Use.
func TestUse_BackupWriteError(t *testing.T) {
	dir := setupFakeHomeInternal(t)
	codexD := filepath.Join(dir, ".codex")
	// Place a foreign auth.json (no id → will try to back it up)
	if err := os.WriteFile(filepath.Join(codexD, "auth.json"), []byte(`{"foreign":"yes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Place bk.auth.json as a directory so WriteFile into it fails, BUT
	// we need Stat(backupPath()) to return IsNotExist so the code tries.
	// Instead: make bk.auth.json a dir with no children → os.Stat returns
	// non-nil, so the outer `os.IsNotExist(err)` branch is false → backup
	// is skipped → we can't reach line 20 this way.
	//
	// Correct approach: make .codex read-only AFTER writing auth.json but
	// BEFORE bk.auth.json exists, so WriteFile(backupPath) fails with
	// permission denied.
	if err := os.Chmod(codexD, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(codexD, 0o755) })

	tok := "h.p.s"
	cred := &store.Credential{
		ID: "id1", Provider: "codex",
		CreatedAt: "2026-05-08T00:00:00Z", LastRefreshedAt: "2026-05-08T00:00:00Z",
		AuthMode: "chatgpt", Tokens: &store.CodexTokens{IDToken: tok, AccessToken: tok, RefreshToken: "rt", AccountID: "a"},
	}
	err := Use(cred)
	if err == nil || !strings.Contains(err.Error(), "backup original auth.json") {
		t.Fatalf("want backup error; got %v", err)
	}
}

// TestWriteActive_WhenActive covers the active path (encodeBlob + Write).
func TestWriteActive_WhenActive(t *testing.T) {
	dir := setupFakeHomeInternal(t)
	tok := "h.p.s"
	cred := &store.Credential{
		ID: "act", Name: "active", Provider: "codex",
		CreatedAt: "2026-05-08T00:00:00Z", LastRefreshedAt: "2026-05-08T00:00:00Z",
		AuthMode: "chatgpt", Tokens: &store.CodexTokens{IDToken: tok, AccessToken: tok, RefreshToken: "rt", AccountID: "a"},
		LastRefresh: "2026-05-08T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	if err := Use(cred); err != nil {
		t.Fatal(err)
	}
	// Now WriteActive should hit the active branch
	if err := WriteActive(cred); err != nil {
		t.Fatalf("WriteActive on active cred: %v", err)
	}
	// Verify the symlink still resolves correctly after WriteActive
	link, err := os.Readlink(filepath.Join(dir, ".codex", "auth.json"))
	if err != nil {
		t.Fatalf("auth.json should still be a symlink: %v", err)
	}
	if !strings.HasSuffix(link, "act.credentials.json") {
		t.Fatalf("unexpected symlink target: %s", link)
	}
}

// TestRestore_RemoveError covers currentBackend().Remove() failure in Restore.
func TestRestore_RemoveError(t *testing.T) {
	dir := setupFakeHomeInternal(t)
	codexD := filepath.Join(dir, ".codex")
	// Write a bk.auth.json so Restore can read it
	bkData := []byte(`{"original":"yes"}`)
	if err := os.WriteFile(filepath.Join(codexD, "bk.auth.json"), bkData, 0o600); err != nil {
		t.Fatal(err)
	}
	// Create auth.json as a non-empty directory so Remove() gets ENOTEMPTY
	authP := filepath.Join(codexD, "auth.json")
	if err := os.MkdirAll(filepath.Join(authP, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Restore()
	if err == nil {
		t.Fatal("want error from Remove; got nil")
	}
	// The error should bubble from Remove (wrapped "codex: remove auth.json")
	if !strings.Contains(err.Error(), "codex: remove auth.json") {
		t.Fatalf("unexpected error: %v", err)
	}
}
