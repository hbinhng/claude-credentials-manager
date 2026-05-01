package cmd

import (
	"errors"
	"testing"
)

func TestUseCallsPreSync(t *testing.T) {
	calls := 0
	orig := useSyncFn
	useSyncFn = func() (bool, error) { calls++; return false, nil }
	defer func() { useSyncFn = orig }()

	preSync()

	if calls != 1 {
		t.Errorf("useSyncFn called %d times, want 1", calls)
	}
}

func TestPreSync_SwallowsSyncError(t *testing.T) {
	orig := useSyncFn
	useSyncFn = func() (bool, error) { return false, errors.New("boom") }
	defer func() { useSyncFn = orig }()

	// Must not panic, must not propagate. The stderr log line is a
	// best-effort UX warning, not a contract worth pinning here.
	preSync()
}
