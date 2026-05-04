package claude

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func claudeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func credentialsPath() string {
	return filepath.Join(claudeDir(), ".credentials.json")
}

func backupPath() string {
	return filepath.Join(claudeDir(), "bk.credentials.json")
}

// Use activates a credential for Claude Code. Writes the blob through
// currentBackend(). On first activation (no ccmSourceId in the existing
// blob) the prior contents are snapshotted to ~/.claude/bk.credentials.json
// so Restore can put them back.
func Use(cred *store.Credential) error {
	if _, err := os.Stat(claudeDir()); os.IsNotExist(err) {
		return fmt.Errorf("~/.claude/ does not exist. Has Claude Code been run before?")
	}

	b := currentBackend()
	if existing, ok, _ := b.Read(); ok {
		id, _, _, _ := decodeBlob(existing)
		if id == "" {
			if _, err := os.Stat(backupPath()); os.IsNotExist(err) {
				if err := os.WriteFile(backupPath(), existing, 0600); err != nil {
					return fmt.Errorf("backup original credentials: %w", err)
				}
				fmt.Println("Original credentials backed up to ~/.claude/bk.credentials.json")
			}
		}
	}

	blob, err := encodeBlob(cred.ID, cred.ClaudeAiOauth)
	if err != nil {
		// coverage: unreachable — encodeBlob never fails on store.OAuthTokens
		return err
	}
	if err := b.Write(blob); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

// WriteActive pushes the given cred's tokens to the active backend
// entry. Used by `ccm refresh` after refreshing the active credential.
func WriteActive(cred *store.Credential) error {
	blob, err := encodeBlob(cred.ID, cred.ClaudeAiOauth)
	if err != nil {
		// coverage: unreachable — encodeBlob never fails on store.OAuthTokens
		return err
	}
	return currentBackend().Write(blob)
}

// Restore undoes activation.
//
// Behavior table:
//
//	blob exists w/ marker (ccm-managed): restore backup or remove entry.
//	blob exists w/o marker (Claude has, ccm hasn't): print message, no-op.
//	blob does not exist:                  no-op silently (nothing to do).
func Restore() error {
	b := currentBackend()
	if !IsManaged() {
		if _, ok, _ := b.Read(); !ok {
			return nil
		}
		fmt.Println("Claude credentials are not managed by ccm.")
		return nil
	}

	if data, err := os.ReadFile(backupPath()); err == nil {
		if err := b.Write(data); err != nil {
			return fmt.Errorf("restore backup: %w", err)
		}
		// Backup removal failure is cosmetic — Claude already has the
		// original blob via the backend; a stale bk.credentials.json
		// will be reused if the user re-runs Restore (idempotent).
		_ = os.Remove(backupPath())
		fmt.Println("Original credentials restored from backup.")
		return nil
	}

	if err := b.Remove(); err != nil {
		return fmt.Errorf("remove credentials: %w", err)
	}
	fmt.Println("No backup found. Active Claude credentials cleared.")
	return nil
}
