//go:build windows

package cmd

import "os"

// pidAlive returns true when a process with pid is running on this
// system. On Windows, os.FindProcess always succeeds (it returns a
// handle regardless of whether the process is live), so this is a
// best-effort check: we treat "FindProcess returned without error"
// as "probably alive". Callers that need stricter guarantees should
// call this then also check for a fresh PID file write.
//
// coverage: unreachable — this build tag is not exercised by CI.
func pidAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
