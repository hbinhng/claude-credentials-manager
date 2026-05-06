package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDir_UsesHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got := Dir()
	want := filepath.Join(tmp, ".ccm", "usage")
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestEnsureDir_CreatesWith0700(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	st, err := os.Stat(filepath.Join(tmp, ".ccm", "usage"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.IsDir() {
		t.Fatalf("not a dir")
	}
	if mode := st.Mode().Perm(); mode != 0700 {
		t.Fatalf("mode = %o, want 0700", mode)
	}
}

func TestSessionPath_JoinsCorrectly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got := SessionPath("abc-123")
	want := filepath.Join(tmp, ".ccm", "usage", "abc-123.ndjson")
	if got != want {
		t.Fatalf("SessionPath() = %q, want %q", got, want)
	}
}
