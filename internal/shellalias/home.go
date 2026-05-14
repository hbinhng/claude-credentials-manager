package shellalias

import (
	"os"
	"path/filepath"
	"strings"
)

// resolveHome returns the absolute path that should hold ccm's alias
// files. Matches the CCM_HOME semantics in CLAUDE.md: CCM_HOME (when
// non-empty) IS the dir; otherwise ~/.ccm.
func resolveHome() string {
	if v := strings.TrimSpace(os.Getenv("CCM_HOME")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ccm" // coverage: unreachable on supported OSes
	}
	return filepath.Join(home, ".ccm")
}
