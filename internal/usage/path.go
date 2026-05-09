package usage

import (
	"os"
	"path/filepath"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Dir returns the usage directory, rooted at ccm's data dir
// (~/.ccm or $CCM_HOME).
func Dir() string {
	return filepath.Join(store.Dir(), "usage")
}

// EnsureDir creates ~/.ccm/usage/ with 0700 if it does not exist.
func EnsureDir() error {
	return os.MkdirAll(Dir(), 0700)
}

// SessionPath returns ~/.ccm/usage/<sessionID>.ndjson.
// Caller is responsible for validating sessionID with IsValidSessionID.
func SessionPath(sessionID string) string {
	return filepath.Join(Dir(), sessionID+".ndjson")
}
