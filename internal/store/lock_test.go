//go:build !windows

package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func setHomeForLockTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".ccm"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWithCredentialLock_Sequential(t *testing.T) {
	setHomeForLockTest(t)
	called := false
	if err := store.WithCredentialLock("abc", func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("fn not invoked")
	}
}

func TestWithCredentialLock_BlocksConcurrent(t *testing.T) {
	setHomeForLockTest(t)
	var inside int32
	var maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.WithCredentialLock("abc", func() error {
				cur := atomic.AddInt32(&inside, 1)
				for {
					old := atomic.LoadInt32(&maxInside)
					if cur <= old || atomic.CompareAndSwapInt32(&maxInside, old, cur) {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				atomic.AddInt32(&inside, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("concurrent entry: max=%d", maxInside)
	}
}

func TestWithCredentialLock_ReleasesOnPanic(t *testing.T) {
	setHomeForLockTest(t)
	func() {
		defer func() { _ = recover() }()
		_ = store.WithCredentialLock("abc", func() error { panic("boom") })
	}()
	done := make(chan struct{})
	go func() {
		_ = store.WithCredentialLock("abc", func() error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lock not released after panic")
	}
}

func TestWithCredentialLock_LockFilePerm0600(t *testing.T) {
	dir := setHomeForLockTest(t)
	if err := store.WithCredentialLock("abc", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ".ccm", "abc.credentials.json.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock perm: got %o want 0600", info.Mode().Perm())
	}
}

func TestWithCredentialLock_PropagatesFnError(t *testing.T) {
	setHomeForLockTest(t)
	wantErr := errSentinel
	got := store.WithCredentialLock("abc", func() error { return wantErr })
	if got != wantErr {
		t.Fatalf("want %v, got %v", wantErr, got)
	}
}

var errSentinel = errors.New("sentinel")

// TestWithCredentialLock_EnsureDirError verifies that an EnsureDir failure
// (caused by HOME pointing at a non-existent path where MkdirAll is blocked)
// is propagated as an error.
func TestWithCredentialLock_EnsureDirError(t *testing.T) {
	// Point HOME at a file (not a dir) so MkdirAll fails.
	dir := t.TempDir()
	blocker := filepath.Join(dir, ".ccm")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	err := store.WithCredentialLock("abc", func() error { return nil })
	if err == nil {
		t.Fatal("expected error when EnsureDir fails, got nil")
	}
}

// TestWithCredentialLock_OpenFileError verifies that an OpenFile failure
// (caused by the .ccm dir being read-only) is propagated as an error.
func TestWithCredentialLock_OpenFileError(t *testing.T) {
	dir := t.TempDir()
	ccmDir := filepath.Join(dir, ".ccm")
	if err := os.MkdirAll(ccmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	// Make the .ccm directory read-only so OpenFile for the lock fails.
	if err := os.Chmod(ccmDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ccmDir, 0o755) })

	err := store.WithCredentialLock("abc", func() error { return nil })
	if err == nil {
		t.Fatal("expected error when lock file cannot be opened, got nil")
	}
}

// TestWithCredentialLock_Timeout verifies that withCredentialLockTimeout
// returns an error when the lock is held and the timeout expires.
func TestWithCredentialLock_Timeout(t *testing.T) {
	setHomeForLockTest(t)

	// Hold the lock from a goroutine.
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = store.WithCredentialLock("timeout-cred", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held // wait until lock is held

	// Try to acquire with a tiny timeout via the exported test helper.
	err := store.WithCredentialLockTimeout("timeout-cred", 150*time.Millisecond, func() error {
		return nil
	})
	close(release)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
