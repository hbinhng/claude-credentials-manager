//go:build !windows

package capture

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr configures cmd to start in a new process group so that
// killProcessGroup can terminate the entire group (including child processes
// spawned by shell scripts).
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the entire process group of cmd, which includes
// any children spawned by the process (e.g. curl or sleep from a shell stub).
// Falls back to killing just the process if the group kill fails.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		// untestable: Run only calls killProcessGroup after cmd.Start() succeeds,
		// at which point cmd.Process is always non-nil. Defensive guard only.
		return
	}
	pgid := cmd.Process.Pid
	// SysProcAttr.Setpgid=true makes the child the leader of its own group,
	// so PGID == PID of the started process.
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		// Fallback: just kill the process itself. Reachable when the process
		// group has already exited between our check and the kill call.
		// untestable: the race window is too narrow to force reliably in tests.
		_ = cmd.Process.Kill()
	}
}
