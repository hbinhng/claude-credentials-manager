package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestRestore_BothBackupsPresent(t *testing.T) {
	dir := setupHomeWithCcm(t)
	for _, cfg := range []struct{ sub, name string }{
		{".claude", "credentials.json"},
		{".codex", "auth.json"},
	} {
		if err := os.MkdirAll(filepath.Join(dir, cfg.sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, cfg.sub, "bk."+cfg.name), []byte(`{"original":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Write a managed-looking blob so claude.Restore sees IsManaged()==true
	// and actually performs the restore.
	tokens := store.OAuthTokens{
		AccessToken:  "tok",
		RefreshToken: "ref",
		ExpiresAt:    9999999999999,
		Scopes:       []string{"user:inference"},
	}
	installActiveBlob(t, "any-id", tokens)

	if err := restoreCmd.RunE(restoreCmd, nil); err != nil {
		t.Fatal(err)
	}

	// claude: restored from bk.credentials.json — content should be original
	claudeGot, err := os.ReadFile(filepath.Join(dir, ".claude", ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claudeGot), `"original":1`) {
		t.Fatalf(".credentials.json not restored: %s", claudeGot)
	}

	// codex: bk.auth.json was written, auth.json must exist (restore wrote it back)
	codexGot, err := os.ReadFile(filepath.Join(dir, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codexGot), `"original":1`) {
		t.Fatalf(".codex/auth.json not restored: %s", codexGot)
	}
}

func TestRestore_CodexOnlyBackupPresent(t *testing.T) {
	dir := setupHomeWithCcm(t)
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "bk.auth.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), []byte(`{"y":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := &bytes.Buffer{}
	restoreCmd.SetOut(buf)
	defer restoreCmd.SetOut(nil)
	if err := restoreCmd.RunE(restoreCmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Restored ~/.codex") {
		t.Fatalf("expected codex restore message, got: %s", buf.String())
	}
}

func TestRestore_NeitherBackupPresent_PrintsHint(t *testing.T) {
	setupHomeWithCcm(t)
	buf := &bytes.Buffer{}
	restoreCmd.SetOut(buf)
	defer restoreCmd.SetOut(nil)
	if err := restoreCmd.RunE(restoreCmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no backup") {
		t.Fatalf("expected hint when no backups exist, got: %s", buf.String())
	}
}
