package codexoauth

import (
	"os/exec"
	"runtime"
)

// OpenBrowserDefault is the platform-aware default for OpenBrowser.
//
// untestable: spawns external GUI binary
func OpenBrowserDefault(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
