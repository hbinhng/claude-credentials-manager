//go:build windows

package shellalias

import (
	"errors"
	"os/exec"
	"strings"
)

func init() {
	pwshResolver = resolvePwshProfile
}

// currentShellHint on Windows is always "pwsh" if PowerShell is
// installed (we don't support CMD persistence).
func currentShellHint() string {
	if _, err := lookPath("pwsh"); err == nil {
		return "pwsh"
	}
	if _, err := lookPath("powershell.exe"); err == nil {
		return "pwsh"
	}
	return ""
}

// resolvePwshProfile invokes the detected PowerShell with -NoProfile
// and reads $PROFILE. We prefer pwsh (PS 7+) when installed, falling
// back to powershell.exe (Windows PowerShell 5.1, present on all
// modern Windows).
func resolvePwshProfile() (string, error) {
	for _, bin := range []string{"pwsh", "powershell.exe"} {
		if _, err := lookPath(bin); err != nil {
			continue
		}
		out, err := exec.Command(bin, "-NoProfile", "-Command", "$PROFILE").Output()
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(out))
		if path != "" {
			return path, nil
		}
	}
	return "", errors.New("could not resolve PowerShell $PROFILE; install pwsh or powershell.exe")
}
