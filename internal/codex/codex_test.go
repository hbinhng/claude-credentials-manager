//go:build !windows

package codex_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func setupFakeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	for _, sub := range []string{".ccm", ".codex"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil { t.Fatal(err) }
	}
	return dir
}

func mkCodexJWT(t *testing.T, accountID string) string {
	t.Helper()
	exp := time.Now().Add(time.Hour).Unix()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(
		`{"email":"u@x.com","exp":` + strconv.FormatInt(exp, 10) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`,
	))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return h + "." + p + "." + s
}

func mkCodexCred(t *testing.T, id, accountID string) *store.Credential {
	t.Helper()
	tok := mkCodexJWT(t, accountID)
	return &store.Credential{
		ID: id, Name: "test", Provider: "codex",
		CreatedAt: "2026-05-08T00:00:00Z", LastRefreshedAt: "2026-05-08T00:00:00Z",
		AuthMode: "chatgpt", OpenAIAPIKey: nil,
		Tokens: &store.CodexTokens{IDToken: tok, AccessToken: tok, RefreshToken: "rt_a.b", AccountID: accountID},
		LastRefresh: "2026-05-08T00:00:00Z",
	}
}

func TestUse_NoCodexDir_Errors(t *testing.T) {
	dir := setupFakeHome(t)
	if err := os.RemoveAll(filepath.Join(dir, ".codex")); err != nil { t.Fatal(err) }
	cred := mkCodexCred(t, "id1", "acct-1")
	if err := codex.Use(cred); err == nil || !strings.Contains(err.Error(), "~/.codex/ does not exist") {
		t.Fatalf("want missing-dir error; got %v", err)
	}
}

func TestUse_FirstTime_BackupsAndActivates(t *testing.T) {
	dir := setupFakeHome(t)
	preexisting := []byte(`{"existing":"yes"}`)
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), preexisting, 0o600); err != nil { t.Fatal(err) }
	cred := mkCodexCred(t, "id1", "acct-1")
	if err := store.Save(cred); err != nil { t.Fatal(err) }
	if err := codex.Use(cred); err != nil { t.Fatal(err) }

	bk, err := os.ReadFile(filepath.Join(dir, ".codex", "bk.auth.json"))
	if err != nil { t.Fatalf("backup not created: %v", err) }
	if string(bk) != string(preexisting) { t.Fatalf("backup content drift") }

	link, err := os.Readlink(filepath.Join(dir, ".codex", "auth.json"))
	if err != nil { t.Fatalf("auth.json should be a symlink: %v", err) }
	if !strings.HasSuffix(link, "id1.credentials.json") { t.Fatalf("symlink target: %s", link) }
}

func TestUse_SecondTime_NoNewBackup(t *testing.T) {
	dir := setupFakeHome(t)
	preexisting := []byte(`{"existing":"yes"}`)
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), preexisting, 0o600); err != nil { t.Fatal(err) }
	c1 := mkCodexCred(t, "id1", "acct-1")
	c2 := mkCodexCred(t, "id2", "acct-2")
	if err := store.Save(c1); err != nil { t.Fatal(err) }
	if err := store.Save(c2); err != nil { t.Fatal(err) }
	if err := codex.Use(c1); err != nil { t.Fatal(err) }
	bk1, _ := os.ReadFile(filepath.Join(dir, ".codex", "bk.auth.json"))
	if err := codex.Use(c2); err != nil { t.Fatal(err) }
	bk2, _ := os.ReadFile(filepath.Join(dir, ".codex", "bk.auth.json"))
	if string(bk1) != string(bk2) { t.Fatalf("backup overwritten on second Use") }
}

func TestActiveAndIsActive(t *testing.T) {
	setupFakeHome(t)
	cred := mkCodexCred(t, "abc", "acct")
	if err := store.Save(cred); err != nil { t.Fatal(err) }
	if err := codex.Use(cred); err != nil { t.Fatal(err) }
	if codex.ActiveID() != "abc" { t.Fatalf("ActiveID: %q", codex.ActiveID()) }
	if !codex.IsActive("abc") { t.Fatal("IsActive false") }
	if codex.IsActive("xyz") { t.Fatal("IsActive true for unrelated id") }
	if !codex.IsManaged() { t.Fatal("IsManaged false") }
}

func TestActiveID_None(t *testing.T) {
	setupFakeHome(t)
	if codex.ActiveID() != "" { t.Fatalf("ActiveID with no auth: %q", codex.ActiveID()) }
	if codex.IsManaged() { t.Fatal("IsManaged true with no auth") }
}

func TestReadActiveBlob_Present(t *testing.T) {
	setupFakeHome(t)
	cred := mkCodexCred(t, "abc", "acct")
	if err := store.Save(cred); err != nil { t.Fatal(err) }
	if err := codex.Use(cred); err != nil { t.Fatal(err) }
	blob, ok, err := codex.ReadActiveBlob()
	if err != nil { t.Fatal(err) }
	if !ok { t.Fatal("not ok") }
	if !strings.Contains(string(blob), `"abc"`) { t.Fatalf("blob missing id: %s", blob) }
}

func TestRestore_AfterUse(t *testing.T) {
	dir := setupFakeHome(t)
	original := []byte(`{"original":"yes"}`)
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), original, 0o600); err != nil { t.Fatal(err) }
	cred := mkCodexCred(t, "abc", "acct")
	if err := store.Save(cred); err != nil { t.Fatal(err) }
	if err := codex.Use(cred); err != nil { t.Fatal(err) }
	if err := codex.Restore(); err != nil { t.Fatal(err) }
	got, err := os.ReadFile(filepath.Join(dir, ".codex", "auth.json"))
	if err != nil { t.Fatal(err) }
	if string(got) != string(original) { t.Fatalf("restore failed") }
	if codex.ActiveID() != "" { t.Fatalf("ActiveID after Restore: %q", codex.ActiveID()) }
}

func TestRestore_NoBackup_Errors(t *testing.T) {
	setupFakeHome(t)
	if err := codex.Restore(); err == nil { t.Fatal("Restore with no backup should error") }
}

func TestRestore_BackupPerm0600(t *testing.T) {
	dir := setupFakeHome(t)
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), []byte(`{}`), 0o644); err != nil { t.Fatal(err) }
	cred := mkCodexCred(t, "abc", "acct")
	if err := store.Save(cred); err != nil { t.Fatal(err) }
	if err := codex.Use(cred); err != nil { t.Fatal(err) }
	info, err := os.Stat(filepath.Join(dir, ".codex", "bk.auth.json"))
	if err != nil { t.Fatal(err) }
	if info.Mode().Perm() != 0o600 { t.Fatalf("perm: got %o", info.Mode().Perm()) }
}

func TestWriteActive_NoOpWhenNotActive(t *testing.T) {
	setupFakeHome(t)
	cred := mkCodexCred(t, "ghost", "acct")
	if err := codex.WriteActive(cred); err != nil { t.Fatal(err) }
}
