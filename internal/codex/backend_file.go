package codex

import (
	"fmt"
	"os"
	"path/filepath"
)

type fileBackend struct{}

func codexDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".codex")
	}
	return "" // coverage: UserHomeDir failure is untestable without OS-level mocking
}

func authPath() string   { return filepath.Join(codexDir(), "auth.json") }
func backupPath() string { return filepath.Join(codexDir(), "bk.auth.json") }

func (fileBackend) Read() ([]byte, bool, error) {
	data, err := os.ReadFile(authPath())
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (fileBackend) Remove() error {
	if err := os.Remove(authPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex: remove auth.json: %w", err)
	}
	return nil
}
