package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// activePath is the absolute path to ~/.ccm/active.json.
func activePath() string {
	return filepath.Join(store.Dir(), "active.json")
}

type activeFile struct {
	ID string `json:"id"`
}

// Active returns the currently-active credential id, or ("", false) when
// no sidecar exists, the sidecar is corrupt, or the recorded id is empty.
func Active() (string, bool) {
	data, err := os.ReadFile(activePath())
	if err != nil {
		return "", false
	}
	var a activeFile
	if err := json.Unmarshal(data, &a); err != nil {
		return "", false
	}
	if a.ID == "" {
		return "", false
	}
	return a.ID, true
}

// SetActive atomically writes ~/.ccm/active.json with the given id.
func SetActive(id string) error {
	if err := store.EnsureDir(); err != nil {
		return err
	}
	data, err := json.Marshal(activeFile{ID: id})
	if err != nil {
		// coverage: unreachable — marshaling a struct{string} never fails
		return err
	}
	tmp := activePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, activePath())
}

// ClearActive removes ~/.ccm/active.json. A missing file is not an error.
func ClearActive() error {
	if err := os.Remove(activePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
