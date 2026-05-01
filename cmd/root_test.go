package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestSyncSkipFor_TrueForExempted(t *testing.T) {
	mkCmd := func(name string) *cobra.Command { return &cobra.Command{Use: name} }
	for _, name := range []string{"completion", "version", "help", "__complete", "__completeNoDesc"} {
		if !syncSkipFor(mkCmd(name)) {
			t.Errorf("syncSkipFor(%q) = false, want true", name)
		}
	}
}

func TestSyncSkipFor_FalseForRegular(t *testing.T) {
	mkCmd := func(name string) *cobra.Command { return &cobra.Command{Use: name} }
	for _, name := range []string{"use", "status", "refresh", "backup", "logout", "rename", "share", "serve", "launch", "login"} {
		if syncSkipFor(mkCmd(name)) {
			t.Errorf("syncSkipFor(%q) = true, want false", name)
		}
	}
}

func TestRootPersistentPreRunE_CallsSyncFn_WhenNotSkipped(t *testing.T) {
	calls := 0
	orig := claudeSyncFn
	claudeSyncFn = func() (bool, error) { calls++; return false, nil }
	defer func() { claudeSyncFn = orig }()

	cmd := &cobra.Command{Use: "use"}
	if err := rootPersistentPreRunE(cmd, nil); err != nil {
		t.Fatalf("rootPersistentPreRunE: %v", err)
	}
	if calls != 1 {
		t.Errorf("claudeSyncFn called %d times, want 1", calls)
	}
}

func TestRootPersistentPreRunE_SkipsForCompletion(t *testing.T) {
	calls := 0
	orig := claudeSyncFn
	claudeSyncFn = func() (bool, error) { calls++; return false, nil }
	defer func() { claudeSyncFn = orig }()

	cmd := &cobra.Command{Use: "completion"}
	if err := rootPersistentPreRunE(cmd, nil); err != nil {
		t.Fatalf("rootPersistentPreRunE: %v", err)
	}
	if calls != 0 {
		t.Errorf("claudeSyncFn called %d times, want 0 for completion", calls)
	}
}

func TestRootPersistentPreRunE_SwallowsSyncError(t *testing.T) {
	orig := claudeSyncFn
	claudeSyncFn = func() (bool, error) { return false, &testErr{"sync boom"} }
	defer func() { claudeSyncFn = orig }()

	cmd := &cobra.Command{Use: "use"}
	if err := rootPersistentPreRunE(cmd, nil); err != nil {
		t.Errorf("rootPersistentPreRunE returned err = %v, want nil (swallow)", err)
	}
}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }
