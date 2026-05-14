package shellalias

import (
	"os"
	"path/filepath"
	"strings"
)

// userHomeDir is a seam over os.UserHomeDir so tests can simulate the
// rare failure mode of an unresolvable home directory.
var userHomeDir = os.UserHomeDir

// resolveHome returns the absolute path that should hold ccm's alias
// files. Matches the CCM_HOME semantics in CLAUDE.md: CCM_HOME (when
// non-empty) IS the dir; otherwise ~/.ccm.
func resolveHome() string {
	if v := strings.TrimSpace(os.Getenv("CCM_HOME")); v != "" {
		return v
	}
	home, err := userHomeDir()
	if err != nil {
		return ".ccm"
	}
	return filepath.Join(home, ".ccm")
}
