//go:build windows

package store_test

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func setHomeForLockTestWin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".ccm"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWithCredentialLock_Windows_Sequential(t *testing.T) {
	setHomeForLockTestWin(t)
	called := false
	if err := store.WithCredentialLock("xyz", func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("fn not invoked")
	}
}

func TestWithCredentialLock_Windows_BlocksConcurrent(t *testing.T) {
	setHomeForLockTestWin(t)
	var inside int32
	var maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.WithCredentialLock("xyz", func() error {
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
		t.Fatalf("Windows lock allowed concurrent entry: max=%d", maxInside)
	}
}
