package claude

import (
	"encoding/json"
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

// CredentialsPath returns the path to ~/.claude/.credentials.json.
func CredentialsPath() string {
	return credentialsPath()
}

// IsManaged reports whether ccm currently owns ~/.claude/.credentials.json.
func IsManaged() bool {
	_, ok := Active()
	return ok
}

// ActiveID returns the active credential id, or "" when none is active.
func ActiveID() string {
	id, _ := Active()
	return id
}

// IsActive reports whether the given credential id is the active one.
func IsActive(id string) bool {
	active, ok := Active()
	return ok && active == id
}

// Use activates a credential for Claude Code. Writes
// ~/.claude/.credentials.json as a plain regular file containing only
// {claudeAiOauth: ...}, then writes ~/.ccm/active.json.
//
// First-use semantics preserved: if a non-ccm-managed file is at the
// target and no backup exists, it is renamed to bk.credentials.json.
func Use(cred *store.Credential) error {
	dir := claudeDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("~/.claude/ does not exist. Has Claude Code been run before?")
	}

	target := credentialsPath()

	// Backup once: only when there's no active.json yet (i.e. nothing
	// is currently ccm-managed) and no prior backup.
	if !IsManaged() {
		if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() {
			if _, err := os.Stat(backupPath()); os.IsNotExist(err) {
				if err := os.Rename(target, backupPath()); err != nil {
					return fmt.Errorf("backup original credentials: %w", err)
				}
				fmt.Println("Original credentials backed up to ~/.claude/bk.credentials.json")
			} else {
				_ = os.Remove(target)
			}
		} else if err == nil {
			// Symlink or other irregular file from a prior install — drop it.
			_ = os.Remove(target)
		}
	}

	if err := writeClaudeCredentials(cred); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := SetActive(cred.ID); err != nil {
		return fmt.Errorf("set active: %w", err)
	}
	return nil
}

// WriteActive writes the cred's tokens to ~/.claude/.credentials.json,
// overwriting whatever is there. Used by `ccm refresh` after refreshing
// the active credential.
func WriteActive(cred *store.Credential) error {
	return writeClaudeCredentials(cred)
}

// Restore removes ~/.claude/.credentials.json and restores
// ~/.claude/bk.credentials.json if it exists. Also clears active.json.
func Restore() error {
	target := credentialsPath()
	if _, err := os.Lstat(target); err != nil {
		return fmt.Errorf("~/.claude/.credentials.json does not exist")
	}

	if !IsManaged() {
		fmt.Println("~/.claude/.credentials.json is not managed by ccm.")
		return nil
	}

	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove credentials: %w", err)
	}

	if _, err := os.Stat(backupPath()); err == nil {
		if err := os.Rename(backupPath(), target); err != nil {
			return fmt.Errorf("restore backup: %w", err)
		}
		fmt.Println("Original credentials restored from backup.")
	} else {
		fmt.Println("No backup found. ~/.claude/.credentials.json removed.")
	}

	if err := ClearActive(); err != nil {
		return fmt.Errorf("clear active: %w", err)
	}
	return nil
}

// writeClaudeCredentials atomically writes a plain regular
// {claudeAiOauth: ...} file at ~/.claude/.credentials.json.
func writeClaudeCredentials(cred *store.Credential) error {
	body := map[string]any{"claudeAiOauth": cred.ClaudeAiOauth}
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return err
	}
	tmp := credentialsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, credentialsPath())
}
