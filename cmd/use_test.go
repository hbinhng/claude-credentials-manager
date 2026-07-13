package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestUseCmd_Grok_ShowsProxyGuidance(t *testing.T) {
	setupHomeWithCcm(t)
	cred := &store.Credential{
		ID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaad", Name: "grok-use", Provider: "grok",
		GrokTokens: &store.GrokTokens{AccessToken: "a", RefreshToken: "r"},
	}
	cred.SetTokens("a", "r", time.Now().Add(time.Hour).UnixMilli())
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	useCmd.SetOut(&out)
	defer useCmd.SetOut(nil)

	// Must not error (grok is proxy-only; there is nothing to activate),
	// and must point the user at the proxy commands.
	if err := useCmd.RunE(useCmd, []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaad"}); err != nil {
		t.Fatalf("ccm use grok should not error, got: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "ccm launch") {
		t.Errorf("guidance should mention `ccm launch`, got: %q", s)
	}
	if !strings.Contains(strings.ToLower(s), "proxy") {
		t.Errorf("guidance should explain grok is proxy-only, got: %q", s)
	}
}

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

func TestUseCmd_Codex_HappyPath(t *testing.T) {
	dir := setupHomeWithCcm(t)
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred := saveCodexCred(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaab", "codex-use-happy")
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}

	if err := useCmd.RunE(useCmd, []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaab"}); err != nil {
		t.Fatal(err)
	}
	if !codex.IsActive("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaab") {
		t.Fatal("codex.IsActive false after ccm use")
	}
}

func TestUseCmd_PreSyncStillRunsForCodex(t *testing.T) {
	dir := setupHomeWithCcm(t)
	calls := 0
	prev := useSyncFn
	useSyncFn = func() (bool, error) { calls++; return false, nil }
	defer func() { useSyncFn = prev }()

	cred := saveCodexCred(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaac", "codex-presync")
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_ = useCmd.RunE(useCmd, []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaac"})
	// preSync runs unconditionally on every ccm use, including codex.
	if calls != 1 {
		t.Fatalf("preSync calls=%d, want 1", calls)
	}
}

func TestUseCmd_UnknownProvider_Errors(t *testing.T) {
	t.Skip("UnmarshalJSON rejects unknown providers; switch default branch unreachable through normal ingestion")
}
