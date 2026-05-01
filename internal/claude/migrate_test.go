package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestMigrate_NoOpWhenActiveExists(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := SetActive("preexisting"); err != nil {
		t.Fatal(err)
	}
	cred := makeCred("never-migrate")
	saveCred(t, cred)
	if err := os.Symlink(store.CredPath(cred.ID), credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	migrate()

	id, ok := Active()
	if !ok || id != "preexisting" {
		t.Errorf("Active() = (%q, %v), want (\"preexisting\", true) — migrate must not overwrite existing sidecar", id, ok)
	}
	info, err := os.Lstat(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was disturbed despite active.json being present")
	}
}

func TestMigrate_State1a_AbsoluteSymlink(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("abs-sym")
	saveCred(t, cred)
	if err := os.Symlink(store.CredPath(cred.ID), credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	migrate()

	id, ok := Active()
	if !ok || id != cred.ID {
		t.Errorf("Active() = (%q, %v), want (%q, true)", id, ok, cred.ID)
	}

	info, err := os.Lstat(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected regular file after migration, got symlink")
	}
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ClaudeAiOauth.AccessToken != cred.ClaudeAiOauth.AccessToken {
		t.Errorf("AccessToken = %q, want %q", parsed.ClaudeAiOauth.AccessToken, cred.ClaudeAiOauth.AccessToken)
	}
}

func TestMigrate_State1b_LegacyRelativeSymlink(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("legacy-rel")
	saveCred(t, cred)
	wrapper := map[string]any{
		"ccmSourceId":   cred.ID,
		"claudeAiOauth": cred.ClaudeAiOauth,
	}
	wrapperData, _ := json.Marshal(wrapper)
	ccmPath := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(ccmPath, wrapperData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ccm.credentials.json", credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	migrate()

	id, ok := Active()
	if !ok || id != cred.ID {
		t.Errorf("Active() = (%q, %v), want (%q, true)", id, ok, cred.ID)
	}
	if _, err := os.Stat(ccmPath); !os.IsNotExist(err) {
		t.Error("legacy ccm.credentials.json still present after migrate")
	}
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
		CCMSourceID   string            `json:"ccmSourceId"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != "" {
		t.Error("ccmSourceId should be stripped from migrated file")
	}
	if parsed.ClaudeAiOauth.AccessToken != cred.ClaudeAiOauth.AccessToken {
		t.Errorf("AccessToken not preserved through migration")
	}
}

func TestMigrate_State2_WindowsWrapperRegularFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("win-wrap")
	saveCred(t, cred)
	wrapper := map[string]any{
		"ccmSourceId":   cred.ID,
		"claudeAiOauth": cred.ClaudeAiOauth,
	}
	data, _ := json.Marshal(wrapper)
	if err := os.WriteFile(credentialsPath(), data, 0600); err != nil {
		t.Fatal(err)
	}

	migrate()

	id, ok := Active()
	if !ok || id != cred.ID {
		t.Errorf("Active() = (%q, %v), want (%q, true)", id, ok, cred.ID)
	}
	out, err := os.ReadFile(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
		CCMSourceID   string            `json:"ccmSourceId"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != "" {
		t.Error("ccmSourceId marker should be stripped after migration")
	}
	if parsed.ClaudeAiOauth.AccessToken != cred.ClaudeAiOauth.AccessToken {
		t.Errorf("AccessToken not preserved through migration")
	}
}

func TestMigrate_State3_PlainFile_NoOp(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	body := []byte(`{"claudeAiOauth":{"accessToken":"unknown","expiresAt":1}}`)
	if err := os.WriteFile(credentialsPath(), body, 0600); err != nil {
		t.Fatal(err)
	}

	migrate()

	if _, ok := Active(); ok {
		t.Error("migrate created an active.json from an unknown plain file")
	}
	out, err := os.ReadFile(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) {
		t.Errorf("migrate modified an unknown file; got %q, want %q", out, body)
	}
}

func TestMigrate_NoCredentialsFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	migrate()
	if _, ok := Active(); ok {
		t.Error("migrate created active.json with no credentials file present")
	}
}

func TestMigrate_State1a_StoreCredMissing(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.Symlink(store.CredPath("ghost"), credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	migrate()

	if _, err := os.Lstat(credentialsPath()); err != nil {
		t.Errorf("credentialsPath disappeared after migrate of broken symlink: %v", err)
	}
}
