package cmd

import (
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
)

// TestShareCommand_ModelAliasConflictRejected verifies that conflicting
// --model-alias patterns (overlapping source globs) are rejected at
// parse time before any session setup is attempted.
func TestShareCommand_ModelAliasConflictRejected(t *testing.T) {
	// "claude-*" and "claude-opus-*" overlap: "claude-opus-4" matches both.
	_, err := alias.Parse([]string{"claude-*=gpt-5", "claude-opus-*=gpt-4"})
	if err == nil {
		t.Fatal("want conflict error for overlapping alias patterns, got nil")
	}
	if !strings.Contains(err.Error(), "overlap") {
		t.Errorf("err = %v; want 'overlap' in message", err)
	}
}
