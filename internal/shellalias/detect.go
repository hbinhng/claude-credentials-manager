package shellalias

import "os/exec"

// lookPath is exec.LookPath under a seam for testability.
var lookPath = exec.LookPath

// Detect returns every supported shell whose binary is on PATH. The
// user's current-shell hint (best-effort, via $SHELL on Unix or pwsh
// always-on Windows) is moved to index 0 when found.
func Detect() []Shell {
	candidates := []Shell{newBash(), newZsh(), newFish(), newPwsh()}
	binaries := map[string][]string{
		"bash": {"bash"},
		"zsh":  {"zsh"},
		"fish": {"fish"},
		"pwsh": {"pwsh", "powershell.exe"},
	}
	var present []Shell
	for _, sh := range candidates {
		for _, bin := range binaries[sh.Name()] {
			if _, err := lookPath(bin); err == nil {
				present = append(present, sh)
				break
			}
		}
	}
	hint := currentShellHint()
	if hint != "" {
		for i, sh := range present {
			if sh.Name() == hint {
				if i != 0 {
					present[0], present[i] = present[i], present[0]
				}
				break
			}
		}
	}
	return present
}
