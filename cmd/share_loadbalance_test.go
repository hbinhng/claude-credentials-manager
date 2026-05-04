package cmd

import (
	"testing"
	"time"
)

func TestRebalanceIntervalDefaults(t *testing.T) {
	d, err := shareCmd.Flags().GetDuration("rebalance-interval")
	if err != nil {
		t.Fatalf("get flag: %v", err)
	}
	if d != 5*time.Minute {
		t.Errorf("default rebalance-interval = %v, want 5m", d)
	}
}

func TestValidateRebalanceInterval(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"too short", "10s", true},
		{"min", "30s", false},
		{"normal", "5m", false},
		{"max", "1h", false},
		{"too long", "2h", true},
		{"unparseable", "bogus", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRebalanceInterval(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("input %q: want err, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("input %q: want nil, got %v", tc.input, err)
			}
		})
	}
}

func TestValidateShareArgs_NoLoadBalance(t *testing.T) {
	if err := shareCmd.Flags().Set("load-balance", "false"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	defer shareCmd.Flags().Set("load-balance", "false")

	if err := validateShareArgs(shareCmd, []string{}); err == nil {
		t.Error("zero args without --load-balance: want error")
	}
	if err := validateShareArgs(shareCmd, []string{"a"}); err != nil {
		t.Errorf("one arg without --load-balance: want nil, got %v", err)
	}
	if err := validateShareArgs(shareCmd, []string{"a", "b"}); err == nil {
		t.Error("two args without --load-balance: want error")
	}
}

func TestValidateShareArgs_WithLoadBalance(t *testing.T) {
	if err := shareCmd.Flags().Set("load-balance", "true"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	defer shareCmd.Flags().Set("load-balance", "false")

	for _, args := range [][]string{{}, {"a"}, {"a", "b", "c"}} {
		if err := validateShareArgs(shareCmd, args); err != nil {
			t.Errorf("args %v with --load-balance: want nil, got %v", args, err)
		}
	}
}
