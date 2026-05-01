package cmd

import (
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
