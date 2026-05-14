//go:build !windows

package shellalias

import (
	"os"
	"path/filepath"
	"strings"
)

// currentShellHint returns "bash", "zsh", "fish", "pwsh", or "" based
// on $SHELL's basename. This is the user's login shell — not the
// running shell — which is the most reliable signal a child process
// has on Unix. Parent-process inspection is unreliable under tmux,
// screen, and IDE terminals so we skip it.
func currentShellHint() string {
	v := strings.TrimSpace(os.Getenv("SHELL"))
	if v == "" {
		return ""
	}
	name := filepath.Base(v)
	switch name {
	case "bash", "zsh", "fish":
		return name
	case "pwsh":
		return "pwsh"
	}
	return ""
}
