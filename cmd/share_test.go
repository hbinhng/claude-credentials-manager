package cmd

import (
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
)

// TestShareCommand_CodexCredRequiresCodexCLI verifies that runShareSingle
// hard-fails with a useful install hint when the credential is a codex
// credential and the codex CLI is not on PATH. The test uses an empty
// PATH so exec.LookPath never succeeds.
func TestShareCommand_CodexCredRequiresCodexCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir — no binaries present
	setupHomeWithCcm(t)
	cred := saveCodexCred(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "codex-share-test")

	err := runShareSingle(cred, share.Options{})
	if err == nil {
		t.Fatal("runShareSingle: nil err, want hard-fail for missing codex CLI")
	}
	if !strings.Contains(err.Error(), "codex CLI is required") {
		t.Errorf("err = %v; want 'codex CLI is required' in message", err)
	}
}

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
