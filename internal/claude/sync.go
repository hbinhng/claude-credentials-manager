package claude

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Sync reconciles ~/.claude/.credentials.json into the active store
// credential. Returns (true, nil) when the store was updated; (false,
// nil) for any no-op (no active sidecar, no claude file, equal/older
// claude data, etc.). Returns an error only when the store write itself
// fails after we've decided a sync is needed.
//
// Best-effort: callers in PreRunE swallow errors; explicit callers
// (ccm backup) propagate them.
func Sync() (bool, error) {
	migrate() // best-effort, swallows its own errors

	id, ok := Active()
	if !ok {
		return false, nil
	}
	cred, err := store.Load(id)
	if err != nil {
		return false, nil
	}

	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		return false, nil
	}
	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false, nil
	}

	if parsed.ClaudeAiOauth.ExpiresAt <= cred.ClaudeAiOauth.ExpiresAt {
		return false, nil
	}

	cred.ClaudeAiOauth = parsed.ClaudeAiOauth
	if err := store.Save(cred); err != nil {
		return false, fmt.Errorf("sync: write store: %w", err)
	}
	return true, nil
}

// migrate is a stub that Task 3 fills in. Until then, Sync's contract
// is unaffected because all current tests provide active.json directly.
func migrate() {}
