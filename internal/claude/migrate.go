package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// migrate converts pre-existing ccm install layouts to the new
// active.json + plain regular file format. Idempotent: short-circuits
// when active.json already exists. All errors are swallowed —
// migration is best-effort.
func migrate() {
	if _, ok := Active(); ok {
		return
	}

	id := detectLegacyState()
	if id == "" {
		return
	}

	cred, err := store.Load(id)
	if err != nil {
		// Store cred missing — record active anyway so future syncs
		// have something to point at; the user can `ccm logout` if
		// they want to clean up. Skip the file rewrite (we don't
		// have data to write).
		_ = SetActive(id)
		_ = cleanupLegacyArtifacts()
		return
	}

	body := map[string]any{"claudeAiOauth": cred.ClaudeAiOauth}
	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	_ = os.Remove(credentialsPath())
	if err := os.WriteFile(credentialsPath(), data, 0600); err != nil {
		return
	}
	_ = SetActive(id)
	_ = cleanupLegacyArtifacts()
}

// detectLegacyState inspects ~/.claude/.credentials.json and returns
// the recovered credential id, or "" when the layout is unrecognized
// (state 3) or no file exists.
func detectLegacyState() string {
	path := credentialsPath()
	info, err := os.Lstat(path)
	if err != nil {
		return ""
	}

	// State 1: symlink.
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return ""
		}
		// 1a: absolute symlink into the store.
		if filepath.IsAbs(target) && strings.HasPrefix(target, store.Dir()+string(filepath.Separator)) {
			base := filepath.Base(target)
			if strings.HasSuffix(base, ".credentials.json") {
				return strings.TrimSuffix(base, ".credentials.json")
			}
		}
		// 1b: legacy relative symlink to ccm.credentials.json.
		if target == "ccm.credentials.json" {
			data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "ccm.credentials.json"))
			if err != nil {
				return ""
			}
			var w struct {
				CCMSourceID string `json:"ccmSourceId"`
			}
			if json.Unmarshal(data, &w) == nil && w.CCMSourceID != "" {
				return w.CCMSourceID
			}
		}
		return ""
	}

	// State 2: regular file with ccmSourceId marker (Windows wrapper).
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var w struct {
		CCMSourceID string `json:"ccmSourceId"`
	}
	if json.Unmarshal(data, &w) == nil && w.CCMSourceID != "" {
		return w.CCMSourceID
	}

	// State 3: unknown plain file → no detection possible.
	return ""
}

// cleanupLegacyArtifacts removes ~/.claude/ccm.credentials.json (the
// legacy intermediate file). Harmless when absent.
func cleanupLegacyArtifacts() error {
	path := filepath.Join(claudeDir(), "ccm.credentials.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
