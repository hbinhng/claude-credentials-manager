package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureFileStub_WritesWhenMissing(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	path := filepath.Join(dir, ".credentials.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pre-condition: expected no file at %s, got err=%v", path, err)
	}

	stubCleanup, err := EnsureFileStub()
	if err != nil {
		t.Fatalf("EnsureFileStub: %v", err)
	}
	defer stubCleanup()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stub: %v", err)
	}
	if !bytes.Equal(got, stubBlob) {
		t.Errorf("stub content mismatch:\n got=%s\nwant=%s", got, stubBlob)
	}
}

func TestEnsureFileStub_NoOpWhenExists(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	path := filepath.Join(dir, ".credentials.json")
	existing := []byte(`{"claudeAiOauth":{"accessToken":"real"}}`)
	if err := os.WriteFile(path, existing, 0600); err != nil {
		t.Fatal(err)
	}

	stubCleanup, err := EnsureFileStub()
	if err != nil {
		t.Fatalf("EnsureFileStub: %v", err)
	}
	defer stubCleanup()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, existing) {
		t.Errorf("existing file was modified:\n got=%s\nwant=%s", got, existing)
	}

	// Cleanup must NOT remove a pre-existing file.
	stubCleanup()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("cleanup removed pre-existing file: %v", err)
	}
}

func TestEnsureFileStub_CleanupRemovesStubExactly(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	stubCleanup, err := EnsureFileStub()
	if err != nil {
		t.Fatalf("EnsureFileStub: %v", err)
	}
	stubCleanup()

	path := filepath.Join(dir, ".credentials.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stub still present after cleanup: err=%v", err)
	}
}

func TestEnsureFileStub_CleanupSkipsWhenModified(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	stubCleanup, err := EnsureFileStub()
	if err != nil {
		t.Fatalf("EnsureFileStub: %v", err)
	}

	path := filepath.Join(dir, ".credentials.json")
	replacement := []byte(`{"claudeAiOauth":{"accessToken":"someone-else"}}`)
	if err := os.WriteFile(path, replacement, 0600); err != nil {
		t.Fatal(err)
	}

	stubCleanup()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after cleanup: %v", err)
	}
	if !bytes.Equal(got, replacement) {
		t.Errorf("cleanup clobbered replacement:\n got=%s\nwant=%s", got, replacement)
	}
}

func TestEnsureFileStub_CleanupTolerantOfMissingFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	stubCleanup, err := EnsureFileStub()
	if err != nil {
		t.Fatalf("EnsureFileStub: %v", err)
	}

	// Simulate someone else removing the stub before cleanup runs.
	path := filepath.Join(dir, ".credentials.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	stubCleanup() // must not panic or error
}

func TestEnsureFileStub_NoOpForBrokenSymlink(t *testing.T) {
	skipIfChmodNoOp(t) // symlink semantics; skip on Windows
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	path := filepath.Join(dir, ".credentials.json")
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), path); err != nil {
		t.Fatal(err)
	}

	stubCleanup, err := EnsureFileStub()
	if err != nil {
		t.Fatalf("EnsureFileStub: %v", err)
	}
	defer stubCleanup()

	// Symlink should be untouched; target should not have been created.
	if _, err := os.Stat(filepath.Join(dir, "nonexistent-target")); !os.IsNotExist(err) {
		t.Errorf("write leaked through symlink: target err=%v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("path is no longer a symlink: mode=%v", info.Mode())
	}
}

func TestEnsureFileStub_PropagatesWriteError(t *testing.T) {
	skipIfChmodNoOp(t)
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	_, err := EnsureFileStub()
	if err == nil {
		t.Error("EnsureFileStub on read-only .claude: nil err, want failure")
	}
}

func TestEnsureFileStub_PropagatesStatError(t *testing.T) {
	skipIfChmodNoOp(t)
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Make the .claude directory non-readable so Lstat on a child path
	// returns EACCES rather than ENOENT.
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	_, err := EnsureFileStub()
	if err == nil {
		t.Skip("os.Lstat did not surface a permission error on this platform")
	}
}
