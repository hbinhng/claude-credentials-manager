package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WithCredentialLock takes an exclusive process-level lock on a sidecar
// file at ~/.ccm/{id}.credentials.json.lock, runs fn, releases the
// lock. Lock auto-releases on process exit (kernel-managed).
func WithCredentialLock(id string, fn func() error) error {
	return WithCredentialLockTimeout(id, 30*time.Second, fn)
}

// WithCredentialLockTimeout is like WithCredentialLock but accepts an
// explicit timeout for the acquisition phase. Exported for testing only.
func WithCredentialLockTimeout(id string, total time.Duration, fn func() error) error {
	if err := EnsureDir(); err != nil {
		return err
	}
	lockPath := filepath.Join(Dir(), id+".credentials.json.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()
	if err := acquireLock(f, total); err != nil {
		return err
	}
	defer releaseLock(f)
	return fn()
}
