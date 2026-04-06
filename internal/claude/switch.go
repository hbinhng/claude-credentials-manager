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

func ccmPath() string {
	return filepath.Join(claudeDir(), "ccm.credentials.json")
}

// ActiveID returns the credential ID currently symlinked, or empty string.
func ActiveID() string {
	target, err := os.Readlink(credentialsPath())
	if err != nil {
		return ""
	}
	if target != "ccm.credentials.json" {
		return ""
	}
	data, err := os.ReadFile(ccmPath())
	if err != nil {
		return ""
	}
	var wrapper struct {
		CCMSourceID string `json:"ccmSourceId"`
	}
	if json.Unmarshal(data, &wrapper) == nil && wrapper.CCMSourceID != "" {
		return wrapper.CCMSourceID
	}
	return ""
}

// IsActive checks if the given credential ID is the currently active one.
func IsActive(id string) bool {
	return ActiveID() == id
}

// WriteActive writes the credential's OAuth tokens to ~/.claude/ccm.credentials.json.
func WriteActive(cred *store.Credential) error {
	wrapper := map[string]any{
		"claudeAiOauth": cred.ClaudeAiOauth,
		"ccmSourceId":   cred.ID,
	}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	tmp := ccmPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, ccmPath())
}

// Use activates a credential by writing it to ~/.claude/ and symlinking.
func Use(cred *store.Credential) error {
	dir := claudeDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("~/.claude/ does not exist. Has Claude Code been run before?")
	}

	target := credentialsPath()
	backup := backupPath()

	// Check if target is a regular file (not a symlink)
	info, err := os.Lstat(target)
	if err == nil && info.Mode().IsRegular() {
		// Backup original if backup doesn't exist yet
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if err := os.Rename(target, backup); err != nil {
				return fmt.Errorf("backup original credentials: %w", err)
			}
			fmt.Println("Original credentials backed up to ~/.claude/bk.credentials.json")
		} else {
			// Backup already exists; remove the current file so the symlink can be created
			os.Remove(target)
		}
	}

	// Remove existing symlink if present
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		os.Remove(target)
	}

	// Write ccm.credentials.json
	if err := WriteActive(cred); err != nil {
		return fmt.Errorf("write ccm credentials: %w", err)
	}

	// Create relative symlink: .credentials.json -> ccm.credentials.json
	if err := os.Symlink("ccm.credentials.json", target); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}

	return nil
}

// Restore removes the CCM symlink and restores the original backup.
func Restore() error {
	target := credentialsPath()
	backup := backupPath()
	ccm := ccmPath()

	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("~/.claude/.credentials.json does not exist")
	}

	if info.Mode()&os.ModeSymlink == 0 {
		fmt.Println("~/.claude/.credentials.json is not managed by CCM (not a symlink).")
		return nil
	}

	// Remove symlink
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove symlink: %w", err)
	}

	// Restore backup if it exists
	if _, err := os.Stat(backup); err == nil {
		if err := os.Rename(backup, target); err != nil {
			return fmt.Errorf("restore backup: %w", err)
		}
		fmt.Println("Original credentials restored from backup.")
	} else {
		fmt.Println("No backup found. ~/.claude/.credentials.json removed.")
	}

	// Clean up ccm.credentials.json
	os.Remove(ccm)

	return nil
}
