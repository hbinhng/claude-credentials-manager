package cmd

import (
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
)

func TestShareCommand_RejectsCodexCred(t *testing.T) {
	setupHomeWithCcm(t)
	cred := saveCodexCred(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "codex-share-test")

	err := runShareSingle(cred, share.Options{})
	if err == nil {
		t.Fatal("runShareSingle: nil err, want rejection for codex cred")
	}
	if !strings.Contains(err.Error(), "claude-only") {
		t.Errorf("err = %v; want 'claude-only' in message", err)
	}
}
