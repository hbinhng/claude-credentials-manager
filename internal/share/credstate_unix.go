//go:build !windows

package share

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// withLock runs fn while holding an exclusive flock on the credential
// file's sibling .lock path. flock(2) is per open-file-description on
// Linux and macOS, so concurrent processes serialize on this lock even
// though no on-disk contents change.
//
// The lock file is created (0600) if absent and never removed — same
// pattern as git and dpkg lock files.
func withLock(id string, fn func() error) error {
	path := filepath.Join(store.Dir(), id+".credentials.json.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
