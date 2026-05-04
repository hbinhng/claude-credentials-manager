package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestMigrate_NoOpWhenActiveAlreadySet(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	setActiveBlob(t, "already-active", store.OAuthTokens{AccessToken: "x", ExpiresAt: 1})

	migrate()

	id, ok := Active()
	if !ok || id != "already-active" {
		t.Errorf("Active() after migrate = (%q, %v), want (\"already-active\", true)", id, ok)
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
		CCMSourceID   string            `json:"ccmSourceId"`
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != cred.ID {
		t.Errorf("CCMSourceID = %q, want %q (must be embedded after migration)", parsed.CCMSourceID, cred.ID)
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
	if parsed.CCMSourceID != cred.ID {
		t.Errorf("CCMSourceID = %q, want %q (must be embedded after migration)", parsed.CCMSourceID, cred.ID)
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
	if parsed.CCMSourceID != cred.ID {
		t.Errorf("CCMSourceID = %q, want %q (must be embedded after migration)", parsed.CCMSourceID, cred.ID)
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
		t.Error("migrate created an active marker from an unknown plain file")
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
		t.Error("migrate created marker with no credentials file present")
	}
}

func TestMigrate_State1a_StoreCredMissing(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	// Plant a legacy artifact alongside the ghost symlink so we can
	// also verify cleanupLegacyArtifacts() ran in this branch.
	legacy := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(legacy, []byte(`{"ccmSourceId":"unrelated"}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(store.CredPath("ghost"), credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	migrate()

	if _, ok := Active(); ok {
		t.Error("Active() should remain unset when store cred is missing")
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy ccm.credentials.json still present; cleanupLegacyArtifacts not called in ghost branch")
	}
}

// detectLegacyState: state 1b — ccm.credentials.json exists but is unreadable.
func TestDetectLegacyState_1b_UnreadableCCMFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	ccmPath := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(ccmPath, []byte(`{"ccmSourceId":"x"}`), 0000); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ccm.credentials.json", credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	if got := detectLegacyState(); got != "" {
		t.Errorf("detectLegacyState() = %q, want \"\" when ccm file is unreadable", got)
	}
}

// detectLegacyState: state 1b — ccm.credentials.json exists but has no ccmSourceId.
func TestDetectLegacyState_1b_NoCCMSourceID(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	ccmPath := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(ccmPath, []byte(`{"claudeAiOauth":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ccm.credentials.json", credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	if got := detectLegacyState(); got != "" {
		t.Errorf("detectLegacyState() = %q, want \"\" when ccmSourceId is absent", got)
	}
}

// detectLegacyState: state 2 — regular file is unreadable.
func TestDetectLegacyState_State2_UnreadableFile(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	path := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(path, []byte(`{"ccmSourceId":"x"}`), 0000); err != nil {
		t.Fatal(err)
	}

	if got := detectLegacyState(); got != "" {
		t.Errorf("detectLegacyState() = %q, want \"\" when file is unreadable", got)
	}
}

// cleanupLegacyArtifacts: os.Remove fails with a non-NotExist error.
func TestCleanupLegacyArtifacts_RemoveFails(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	ccmPath := filepath.Join(dir, "ccm.credentials.json")
	if err := os.WriteFile(ccmPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	if err := cleanupLegacyArtifacts(); err == nil {
		t.Fatal("cleanupLegacyArtifacts: nil err, want remove failure")
	}
}

// migrate: Write through fileBackend fails (claudeDir unwritable).
func TestMigrate_WriteFails(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("wrfail")
	saveCred(t, cred)
	if err := os.Symlink(store.CredPath(cred.ID), credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	migrate() // must not panic; error is swallowed

	if _, ok := Active(); ok {
		t.Error("Active should not be set when backend write fails during migrate")
	}
}

// migrate: Rename of tmp to target fails (replace the tmp file with
// an unremovable entry so os.Rename fails — achieved by making it a dir).
func TestMigrate_RenameFails(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("rnfail")
	saveCred(t, cred)
	if err := os.Symlink(store.CredPath(cred.ID), credentialsPath()); err != nil {
		t.Skip("symlink unsupported")
	}

	tmpPath := credentialsPath() + ".tmp"
	if err := os.MkdirAll(tmpPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpPath) })

	migrate() // must not panic; error is swallowed

	if _, ok := Active(); ok {
		t.Error("Active should not be set when backend write fails during migrate")
	}
}

// State 4: ~/.ccm/active.json + plain blob at credentialsPath. Migration
// must rewrite the file with the embedded marker and delete active.json.
func TestMigrate_State4_ActiveSidecarPlusPlainBlob(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	cred := makeCred("modern")
	saveCred(t, cred)

	if err := os.MkdirAll(filepath.Dir(activeSidecarPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activeSidecarPath(), []byte(`{"id":"modern"}`), 0600); err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(map[string]any{"claudeAiOauth": cred.ClaudeAiOauth})
	if err := os.WriteFile(credentialsPath(), plain, 0600); err != nil {
		t.Fatal(err)
	}

	migrate()

	if _, err := os.Stat(activeSidecarPath()); !os.IsNotExist(err) {
		t.Error("~/.ccm/active.json still present after migrate")
	}

	id, ok := Active()
	if !ok || id != cred.ID {
		t.Errorf("Active() = (%q, %v), want (%q, true)", id, ok, cred.ID)
	}
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		CCMSourceID   string            `json:"ccmSourceId"`
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CCMSourceID != cred.ID {
		t.Errorf("CCMSourceID = %q, want %q", parsed.CCMSourceID, cred.ID)
	}
	if parsed.ClaudeAiOauth.AccessToken != cred.ClaudeAiOauth.AccessToken {
		t.Errorf("AccessToken not preserved")
	}
}

func TestMigrate_State4_StoreCredMissing(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.MkdirAll(filepath.Dir(activeSidecarPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activeSidecarPath(), []byte(`{"id":"missing"}`), 0600); err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"claudeAiOauth":{"accessToken":"x","expiresAt":1}}`)
	if err := os.WriteFile(credentialsPath(), plain, 0600); err != nil {
		t.Fatal(err)
	}

	migrate()

	if _, err := os.Stat(activeSidecarPath()); !os.IsNotExist(err) {
		t.Error("~/.ccm/active.json should be cleaned up even when store cred missing")
	}
	if _, ok := Active(); ok {
		t.Error("Active() should remain unset when store cred missing")
	}
}
