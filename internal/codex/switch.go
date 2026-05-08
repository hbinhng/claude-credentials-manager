package codex

import (
	"fmt"
	"os"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Use activates cred for the codex CLI. Backs up any existing foreign
// auth.json on first activation. Refuses if ~/.codex/ doesn't exist.
func Use(cred *store.Credential) error {
	if _, err := os.Stat(codexDir()); os.IsNotExist(err) {
		return fmt.Errorf("~/.codex/ does not exist. Has codex CLI been run before?")
	}
	b := currentBackend()
	if existing, ok, _ := b.Read(); ok {
		if id, _ := decodeBlob(existing); id == "" {
			if _, err := os.Stat(backupPath()); os.IsNotExist(err) {
				if err := os.WriteFile(backupPath(), existing, 0o600); err != nil {
					return fmt.Errorf("codex: backup original auth.json: %w", err)
				}
			}
		}
	}
	blob, err := encodeBlob(cred)
	if err != nil {
		return err
	}
	return b.Write(blob)
}

// WriteActive resyncs the active codex auth.json. On Unix the symlink
// makes this a no-op when active; on Windows the wrapper copy needs
// an explicit refresh.
func WriteActive(cred *store.Credential) error {
	if !IsActive(cred.ID) {
		return nil
	}
	blob, err := encodeBlob(cred)
	if err != nil {
		// coverage: unreachable — IsActive returned true, so cred is codex
		// (only codex creds can be activated) and encodeBlob only fails for
		// non-codex providers. This branch exists for defensive completeness.
		return err
	}
	return currentBackend().Write(blob)
}

// Restore restores the original ~/.codex/auth.json from bk.auth.json.
func Restore() error {
	bk, err := os.ReadFile(backupPath())
	if err != nil {
		return fmt.Errorf("codex: read backup: %w", err)
	}
	if err := currentBackend().Remove(); err != nil {
		return err
	}
	return os.WriteFile(authPath(), bk, 0o600)
}
