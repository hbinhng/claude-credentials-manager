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

// resolvePwshProfile asks every installed PowerShell host for its
// $PROFILE and returns the union. When both pwsh (PS 7+) and
// powershell.exe (PS 5.1) are present on Windows, both profiles need
// the source snippet — otherwise the user opens the "other" host and
// the alias is silently absent.
func resolvePwshProfile() ([]string, error) {
	var profiles []string
	seen := map[string]bool{}
	for _, bin := range []string{"pwsh", "powershell.exe"} {
		if _, err := lookPath(bin); err != nil {
			continue
		}
		out, err := exec.Command(bin, "-NoProfile", "-Command", "$PROFILE").Output()
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(out))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		profiles = append(profiles, path)
	}
	if len(profiles) == 0 {
		return nil, errors.New("could not resolve PowerShell $PROFILE; install pwsh or powershell.exe")
	}
	return profiles, nil
}
