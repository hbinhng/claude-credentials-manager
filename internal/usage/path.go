package usage

import (
	"os"
	"path/filepath"
)

// Dir returns ~/.ccm/usage/.
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccm", "usage")
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
