//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func acquireLock(f *os.File, total time.Duration) error {
	handle := windows.Handle(f.Fd())
	overlapped := new(windows.Overlapped)
	deadline := time.Now().Add(total)
	delays := []time.Duration{50, 100, 200, 400, 800}
	idx := 0
	for {
		err := windows.LockFileEx(handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, overlapped)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			return fmt.Errorf("LockFileEx: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("another ccm process holds the lock for credential %s", f.Name())
		}
		time.Sleep(delays[idx] * time.Millisecond)
		if idx < len(delays)-1 {
			idx++
		}
	}
}

func releaseLock(f *os.File) {
	handle := windows.Handle(f.Fd())
	overlapped := new(windows.Overlapped)
	_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
}
