package claude

import (
	"fmt"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Sync reconciles the active backend entry into the matching store
// credential. Returns (true, nil) when the store was updated;
// (false, nil) for any no-op. Returns an error only when the store
// write itself fails after we've decided a sync is needed.
//
// Best-effort: callers in PreRunE swallow errors; explicit callers
// (ccm backup) propagate them.
func Sync() (bool, error) {
	migrate() // best-effort, swallows its own errors

	blob, ok, err := currentBackend().Read()
	if err != nil || !ok {
		return false, nil
	}
	id, claudeTokens, hasTokens, err := decodeBlob(blob)
	if err != nil || id == "" || !hasTokens {
		return false, nil
	}

	cred, err := store.Load(id)
	if err != nil {
		return false, nil
	}

	if claudeTokens.ExpiresAt <= cred.ClaudeAiOauth.ExpiresAt {
		return false, nil
	}

	cred.ClaudeAiOauth = claudeTokens
	if err := store.Save(cred); err != nil {
		return false, fmt.Errorf("sync: write store: %w", err)
	}
	return true, nil
}
