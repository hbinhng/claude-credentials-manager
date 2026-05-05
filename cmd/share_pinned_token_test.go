package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
)

func TestReadPinnedTokenFromEnv(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		val  string
		want string
	}{
		{"unset", false, "", ""},
		{"empty", true, "", ""},
		{"whitespace", true, "   ", ""},
		{"tab+newline", true, "\t\n", ""},
		{"valid", true, "abc123", "abc123"},
		{"trim valid", true, "  abc123  ", "abc123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("CCM_SHARE_TOKEN", tc.val)
			} else {
				// Make sure the parent process's value (if any) does
				// not leak in. t.Setenv("CCM_SHARE_TOKEN", "") still
				// sets it; we want unset, so we Setenv to empty and
				// rely on the trim behaviour.
				t.Setenv("CCM_SHARE_TOKEN", "")
			}
			got := readPinnedTokenFromEnv()
			if got != tc.want {
				t.Errorf("readPinnedTokenFromEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWrapPinnedTokenErr(t *testing.T) {
	if got := wrapPinnedTokenErr(nil); got != nil {
		t.Errorf("nil err round-tripped to %v", got)
	}

	plainErr := errors.New("some other failure")
	if got := wrapPinnedTokenErr(plainErr); got != plainErr {
		t.Errorf("plain err was wrapped: %v", got)
	}

	// Compose an error that wraps share.ErrInvalidPinnedToken just
	// like StartSession would surface it.
	pinErr := share.ValidatePinnedToken("has space")
	if pinErr == nil {
		t.Fatalf("ValidatePinnedToken returned nil for invalid input")
	}
	wrapped := wrapPinnedTokenErr(pinErr)
	if wrapped == nil {
		t.Fatalf("wrapped err is nil")
	}
	if !errors.Is(wrapped, share.ErrInvalidPinnedToken) {
		t.Errorf("wrapped err does not chain to ErrInvalidPinnedToken: %v", wrapped)
	}
	if !strings.HasPrefix(wrapped.Error(), "CCM_SHARE_TOKEN:") {
		t.Errorf("wrapped err message %q, want prefix CCM_SHARE_TOKEN:", wrapped.Error())
	}
}
