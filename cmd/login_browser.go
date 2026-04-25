package cmd

import (
	"os/exec"
	"runtime"
)

// tryOpenBrowser launches the platform's URL handler against url and
// returns immediately. Failures are silent — the caller has already
// printed the URL to stdout, so the user can copy it manually.
//
// Wrapped behind tryOpenBrowserFn so tests can assert the call without
// shelling out to xdg-open / open.
func tryOpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return
	}
	_ = cmd.Start()
}

var tryOpenBrowserFn = tryOpenBrowser
