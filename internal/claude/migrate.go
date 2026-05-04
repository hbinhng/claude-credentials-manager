package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// migrate converts pre-existing ccm install layouts to the new
// {ccmSourceId, claudeAiOauth} blob format written through the
// CURRENTLY ACTIVE backend (not necessarily the file). This matters
// when the user has just upgraded both Claude (which moved to the
// keychain) and ccm (which probes keychain first): legacy file-based
// state must be migrated INTO the keychain so ccm and Claude end up
// reading from the same place.
//
// Idempotent: short-circuits the rewrite when Active() already returns
// true AND the on-disk shape is canonical (regular file for file
// backend). cleanupLegacyArtifacts always runs so leftover side files
// (ccm.credentials.json, active.json) are removed even when no rewrite
// is needed.
func migrate() {
	if _, ok := Active(); ok {
		// Already in marker state. For the file backend, also normalize
		// shape: rewrite a symlink at credentialsPath as a regular file.
		if _, isFile := currentBackend().(fileBackend); isFile {
			if info, err := os.Lstat(credentialsPath()); err == nil && info.Mode()&os.ModeSymlink != 0 {
				if blob, hasBlob, _ := (fileBackend{}).Read(); hasBlob {
					_ = (fileBackend{}).Write(blob)
				}
			}
		}
		_ = cleanupLegacyArtifacts()
		return
	}

	id := detectLegacyState()
	if id == "" {
		_ = cleanupLegacyArtifacts()
		return
	}

	cred, err := store.Load(id)
	if err != nil {
		_ = cleanupLegacyArtifacts()
		return
	}

	blob, err := encodeBlob(cred.ID, cred.ClaudeAiOauth)
	if err != nil {
		// coverage: unreachable — encodeBlob never fails on store.OAuthTokens
		return
	}
	b := currentBackend()
	if err := b.Write(blob); err != nil {
		return
	}
	if _, isFile := b.(fileBackend); !isFile {
		_ = os.Remove(credentialsPath())
	}
	_ = cleanupLegacyArtifacts()
}

// detectLegacyState recognises pre-current layouts and returns the
// recovered credential id, or "" when no legacy state matches.
//
// Precedence (intentional): state 4 wins over states 1-3. Rationale:
// active.json is the explicit, ccm-controlled marker from the most
// recent layout; if it's present we trust it over inferring from the
// shape of credentialsPath, which could be a symlink to an unrelated
// store cred from an even-earlier install.
func detectLegacyState() string {
	// State 4: ~/.ccm/active.json present (any shape at credentialsPath).
	if id, ok := readActiveSidecar(); ok {
		return id
	}

	path := credentialsPath()
	info, err := os.Lstat(path)
	if err != nil {
		return ""
	}

	// State 1: symlink.
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			// coverage: unreachable — Readlink on a confirmed symlink never
			// fails on a healthy filesystem; this is purely defensive.
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

	// State 2: regular file with ccmSourceId marker (Windows wrapper layout).
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

// readActiveSidecar returns ("", false) when ~/.ccm/active.json is
// absent, corrupt, or empty. Used only to detect state 4 during
// migration; not part of the runtime path.
func readActiveSidecar() (string, bool) {
	data, err := os.ReadFile(activeSidecarPath())
	if err != nil {
		return "", false
	}
	var a struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &a) != nil || a.ID == "" {
		return "", false
	}
	return a.ID, true
}

func activeSidecarPath() string {
	return filepath.Join(store.Dir(), "active.json")
}

// cleanupLegacyArtifacts removes ~/.claude/ccm.credentials.json (the
// legacy intermediate file) and ~/.ccm/active.json (the modern sidecar
// from before the embedded marker was reintroduced). Harmless when
// absent.
func cleanupLegacyArtifacts() error {
	for _, p := range []string{
		filepath.Join(claudeDir(), "ccm.credentials.json"),
		activeSidecarPath(),
	} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
