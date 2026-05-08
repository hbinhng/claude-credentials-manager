//go:build !windows

package store

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func acquireLock(f *os.File, total time.Duration) error {
	deadline := time.Now().Add(total)
	delays := []time.Duration{50, 100, 200, 400, 800}
	idx := 0
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != unix.EWOULDBLOCK {
			// untestable: requires flock to fail with a non-EWOULDBLOCK errno
			// (e.g. EBADF), which cannot be triggered via normal file operations.
			return fmt.Errorf("flock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("another ccm process holds the lock for credential %s. Wait or kill that process.", f.Name())
		}
		time.Sleep(delays[idx] * time.Millisecond)
		if idx < len(delays)-1 {
			idx++
		}
	}
}

func releaseLock(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
