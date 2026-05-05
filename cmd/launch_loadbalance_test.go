package cmd

import (
	"testing"
	"time"
)

func TestLaunchLoadBalanceFlagDefault(t *testing.T) {
	v, err := launchCmd.Flags().GetBool("load-balance")
	if err != nil {
		t.Fatalf("get flag: %v", err)
	}
	if v {
		t.Errorf("default load-balance = true, want false")
	}
}

func TestLaunchRebalanceIntervalDefault(t *testing.T) {
	d, err := launchCmd.Flags().GetDuration("rebalance-interval")
	if err != nil {
		t.Fatalf("get flag: %v", err)
	}
	if d != 5*time.Minute {
		t.Errorf("default rebalance-interval = %v, want 5m", d)
	}
}

func TestValidateLaunchArgs(t *testing.T) {
	// Reset flags between cases. Save original values so other tests
	// in this binary aren't disturbed.
	origVia, _ := launchCmd.Flags().GetString("via")
	origLB, _ := launchCmd.Flags().GetBool("load-balance")
	t.Cleanup(func() {
		_ = launchCmd.Flags().Set("via", origVia)
		if origLB {
			_ = launchCmd.Flags().Set("load-balance", "true")
		} else {
			_ = launchCmd.Flags().Set("load-balance", "false")
		}
	})

	reset := func() {
		_ = launchCmd.Flags().Set("load-balance", "false")
		_ = launchCmd.Flags().Set("via", "")
	}

	tests := []struct {
		name        string
		viaSet      string // empty = not set
		loadBalance bool
		args        []string
		wantErr     bool
	}{
		{"single-cred, one arg", "", false, []string{"alice"}, false},
		{"single-cred, zero args", "", false, []string{}, true},
		{"single-cred, two args", "", false, []string{"a", "b"}, true},
		{"via, no args", "ticket", false, []string{}, false},
		{"via, with args", "ticket", false, []string{"a"}, true},
		{"load-balance, zero args", "", true, []string{}, false},
		{"load-balance, one arg", "", true, []string{"alice"}, false},
		{"load-balance, multiple args", "", true, []string{"a", "b", "c"}, false},
		{"load-balance + via", "ticket", true, []string{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			if tc.viaSet != "" {
				_ = launchCmd.Flags().Set("via", tc.viaSet)
			}
			if tc.loadBalance {
				_ = launchCmd.Flags().Set("load-balance", "true")
			}
			err := validateLaunchArgs(launchCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("want err, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("want nil, got %v", err)
			}
		})
	}
}
