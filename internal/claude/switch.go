package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

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

func useSymlinks() bool {
	return runtime.GOOS != "windows"
}

// isCCMManaged checks whether .credentials.json is managed by CCM.
// On Unix this means it's a symlink to ccm.credentials.json.
// On Windows (no symlinks) it checks for the ccmSourceId marker inside the file.
func isCCMManaged(path string) bool {
	if useSymlinks() {
		target, err := os.Readlink(path)
		return err == nil && target == "ccm.credentials.json"
	}
	// Windows: check file content for marker
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var wrapper struct {
		CCMSourceID string `json:"ccmSourceId"`
	}
	return json.Unmarshal(data, &wrapper) == nil && wrapper.CCMSourceID != ""
}

// ActiveID returns the credential ID currently active, or empty string.
func ActiveID() string {
	if !isCCMManaged(credentialsPath()) {
		return ""
	}
	// On symlink systems, read from ccm.credentials.json (the symlink target).
	// On Windows, .credentials.json IS the copy, so read it directly.
	var data []byte
	var err error
	if useSymlinks() {
		data, err = os.ReadFile(ccmPath())
	} else {
		data, err = os.ReadFile(credentialsPath())
	}
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

// writeCredentialsFile writes the ccm wrapper JSON directly to .credentials.json.
// Used on Windows where symlinks are not available.
func writeCredentialsFile(cred *store.Credential) error {
	wrapper := map[string]any{
		"claudeAiOauth": cred.ClaudeAiOauth,
		"ccmSourceId":   cred.ID,
	}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	tmp := credentialsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, credentialsPath())
}

// Use activates a credential for Claude Code.
// On Unix: writes ccm.credentials.json and symlinks .credentials.json to it.
// On Windows: writes ccm.credentials.json and copies the content to .credentials.json.
func Use(cred *store.Credential) error {
	dir := claudeDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("~/.claude/ does not exist. Has Claude Code been run before?")
	}

	target := credentialsPath()
	backup := backupPath()

	// Check if target exists and is NOT managed by CCM
	info, err := os.Lstat(target)
	if err == nil && !isCCMManaged(target) && info.Mode().IsRegular() {
		// Backup original if backup doesn't exist yet
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if err := os.Rename(target, backup); err != nil {
				return fmt.Errorf("backup original credentials: %w", err)
			}
			fmt.Println("Original credentials backed up to ~/.claude/bk.credentials.json")
		} else {
			// Backup already exists; remove the current file
			os.Remove(target)
		}
	}

	// Remove existing symlink if present (Unix only)
	if useSymlinks() {
		if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			os.Remove(target)
		}
	}

	// Write ccm.credentials.json
	if err := WriteActive(cred); err != nil {
		return fmt.Errorf("write ccm credentials: %w", err)
	}

	if useSymlinks() {
		// Unix: create relative symlink .credentials.json -> ccm.credentials.json
		if err := os.Symlink("ccm.credentials.json", target); err != nil {
			return fmt.Errorf("create symlink: %w", err)
		}
	} else {
		// Windows: copy content directly to .credentials.json
		if err := writeCredentialsFile(cred); err != nil {
			return fmt.Errorf("write credentials: %w", err)
		}
	}

	return nil
}

// Restore deactivates CCM and restores the original credentials.
func Restore() error {
	target := credentialsPath()
	backup := backupPath()
	ccm := ccmPath()

	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("~/.claude/.credentials.json does not exist")
	}

	if !isCCMManaged(target) {
		if useSymlinks() && info.Mode()&os.ModeSymlink == 0 {
			fmt.Println("~/.claude/.credentials.json is not managed by CCM (not a symlink).")
		} else {
			fmt.Println("~/.claude/.credentials.json is not managed by CCM.")
		}
		return nil
	}

	// Remove .credentials.json (symlink or copy)
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove credentials: %w", err)
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
