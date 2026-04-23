//go:build !windows

package cmd

import (
	"errors"
	"syscall"
)

// pidAlive returns true when a process with pid is running on this
// system. Uses signal 0 which is a permission-check only probe — it
// does not deliver a signal; it returns ESRCH on dead pids, and
// EPERM when the process is alive but owned by a different user.
// Both the "exists and we can signal" and "exists but foreign owner"
// cases count as alive; only ESRCH (or anything else) means dead.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
