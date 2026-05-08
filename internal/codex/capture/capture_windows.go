//go:build windows

package capture

import "os/exec"

// setSysProcAttr is a no-op on Windows. Windows job objects or taskkill
// could be used for full process-tree cleanup, but shell-stub tests are
// not expected to run on Windows (codex CLI stubs use bash).
func setSysProcAttr(cmd *exec.Cmd) {}

// killProcessGroup kills just the immediate process on Windows.
// Child processes spawned by the process may outlive their parent briefly.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
