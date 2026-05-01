package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActive_MissingFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestActive_RoundTrip(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := SetActive("the-id"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	id, ok := Active()
	if !ok || id != "the-id" {
		t.Errorf("Active() = (%q, %v), want (\"the-id\", true)", id, ok)
	}
}

func TestActive_Clear(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := SetActive("x"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := ClearActive(); err != nil {
		t.Fatalf("ClearActive: %v", err)
	}
	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() after clear = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestClearActive_MissingIsNoError(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	if err := ClearActive(); err != nil {
		t.Errorf("ClearActive on missing file: %v, want nil", err)
	}
}

func TestActive_CorruptFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.WriteFile(activePath(), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() with corrupt file = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestActive_EmptyID(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.WriteFile(activePath(), []byte(`{"id":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() with empty id = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestSetActive_AtomicAndModeSecure(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := SetActive("perm"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	info, err := os.Stat(activePath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
	// No leftover .tmp.
	tmp := activePath() + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("leftover %s after SetActive", filepath.Base(tmp))
	}
}
