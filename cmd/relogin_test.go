package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func runReloginCmd(t *testing.T, idOrName string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	reloginCmd.SetOut(&out)
	reloginCmd.SetContext(context.Background())
	err := runRelogin(reloginCmd, []string{idOrName})
	return out.String(), err
}

func TestRelogin_Grok_GraftsOntoExistingIdentity(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{
		ID: "g-existing-0001", Name: "my-grok", Provider: "grok",
		CreatedAt:  "2026-01-01T00:00:00Z",
		GrokTokens: &store.GrokTokens{AccessToken: "old-a", RefreshToken: "old-r"},
	}
	existing.SetTokens("old-a", "old-r", time.Now().Add(-time.Minute).UnixMilli())
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	restore := SeamGrokLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		c := &store.Credential{ID: "g-fresh-9999", Name: "fresh@x.ai", Provider: "grok",
			GrokTokens: &store.GrokTokens{AccessToken: "new-a", RefreshToken: "new-r"}}
		c.SetTokens("new-a", "new-r", time.Now().Add(time.Hour).UnixMilli())
		return c, nil
	})
	defer restore()

	out, err := runReloginCmd(t, "g-existing-0001")
	if err != nil {
		t.Fatalf("relogin: %v", err)
	}

	got, err := store.Load("g-existing-0001")
	if err != nil {
		t.Fatalf("existing cred gone: %v", err)
	}
	if got.AccessToken() != "new-a" || got.RefreshToken() != "new-r" {
		t.Fatalf("tokens not refreshed: %q/%q", got.AccessToken(), got.RefreshToken())
	}
	if got.Name != "my-grok" {
		t.Fatalf("name should be preserved, got %q", got.Name)
	}
	if got.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("CreatedAt not preserved: %q", got.CreatedAt)
	}
	if _, err := store.Load("g-fresh-9999"); err == nil {
		t.Fatal("orphan fresh-id credential should not exist")
	}
	if list, _ := store.List(); len(list) != 1 {
		t.Fatalf("want exactly 1 credential, got %d", len(list))
	}
	if !strings.Contains(out, "my-grok") {
		t.Errorf("output should name the credential; got %q", out)
	}
}

func TestRelogin_Codex_GraftsOntoExistingIdentity(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{
		ID: "x-existing-0001", Name: "my-codex", Provider: "codex", AuthMode: "chatgpt",
		CreatedAt: "2026-01-01T00:00:00Z",
		Tokens:    &store.CodexTokens{AccessToken: "old-a", RefreshToken: "old-r", AccountID: "acct"},
	}
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	restore := SeamCodexLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		return &store.Credential{ID: "x-fresh-9999", Name: "fresh@openai", Provider: "codex", AuthMode: "chatgpt",
			Tokens: &store.CodexTokens{AccessToken: "new-a", RefreshToken: "new-r", AccountID: "acct2"}}, nil
	})
	defer restore()

	if _, err := runReloginCmd(t, "x-existing-0001"); err != nil {
		t.Fatalf("relogin: %v", err)
	}

	got, _ := store.Load("x-existing-0001")
	if got.AccessToken() != "new-a" {
		t.Fatalf("codex tokens not refreshed: %q", got.AccessToken())
	}
	if got.Name != "my-codex" {
		t.Fatalf("name not preserved: %q", got.Name)
	}
	if list, _ := store.List(); len(list) != 1 {
		t.Fatalf("want 1 credential, got %d", len(list))
	}
}

func TestRelogin_Claude_GraftsAndCleansOrphan(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{
		ID: "c-existing-0001", Name: "work",
		ClaudeAiOauth: store.OAuthTokens{AccessToken: "old-a", RefreshToken: "old-r", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()},
		CreatedAt:     "2026-01-01T00:00:00Z",
	}
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	prevBrowser := tryOpenBrowserFn
	tryOpenBrowserFn = func(string) {}
	defer func() { tryOpenBrowserFn = prevBrowser }()

	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example/auth"}
	restore := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(h *credflow.Handshake, code string) (*store.Credential, error) {
			if code != "THECODE" {
				t.Errorf("complete got code %q", code)
			}
			// Mimic credflow.CompleteLogin: mint AND persist a fresh cred.
			nc := &store.Credential{ID: "c-fresh-9999", Name: "fresh@anthropic",
				ClaudeAiOauth: store.OAuthTokens{AccessToken: "new-a", RefreshToken: "new-r", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()},
				CreatedAt:     "2026-07-01T00:00:00Z"}
			if err := store.Save(nc); err != nil {
				t.Fatal(err)
			}
			return nc, nil
		},
		strings.NewReader("THECODE\n"),
	)
	defer restore()

	if _, err := runReloginCmd(t, "c-existing-0001"); err != nil {
		t.Fatalf("relogin: %v", err)
	}

	got, _ := store.Load("c-existing-0001")
	if got.ClaudeAiOauth.AccessToken != "new-a" {
		t.Fatalf("claude tokens not refreshed: %q", got.ClaudeAiOauth.AccessToken)
	}
	if got.Name != "work" {
		t.Fatalf("name not preserved: %q", got.Name)
	}
	if _, err := store.Load("c-fresh-9999"); err == nil {
		t.Fatal("orphan credential from CompleteLogin should have been deleted")
	}
	if list, _ := store.List(); len(list) != 1 {
		t.Fatalf("want 1 credential, got %d", len(list))
	}
}

func TestRelogin_Codex_SyncsActiveCopy(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{ID: "x-active-0001", Name: "active-codex", Provider: "codex", AuthMode: "chatgpt",
		Tokens: &store.CodexTokens{AccessToken: "old-a", RefreshToken: "old-r", AccountID: "acct"}}
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	prevIsActive, prevWrite := codexIsActiveFn, codexWriteActiveFn
	var wroteID string
	codexIsActiveFn = func(id string) bool { return id == "x-active-0001" }
	codexWriteActiveFn = func(c *store.Credential) error { wroteID = c.ID; return nil }
	defer func() { codexIsActiveFn, codexWriteActiveFn = prevIsActive, prevWrite }()

	restore := SeamCodexLogin(func(context.Context, io.Writer, io.Reader) (*store.Credential, error) {
		return &store.Credential{ID: "x-active-fresh", Name: "fresh", Provider: "codex", AuthMode: "chatgpt",
			Tokens: &store.CodexTokens{AccessToken: "new-a", RefreshToken: "new-r", AccountID: "acct"}}, nil
	})
	defer restore()

	if _, err := runReloginCmd(t, "x-active-0001"); err != nil {
		t.Fatalf("relogin: %v", err)
	}
	if wroteID != "x-active-0001" {
		t.Fatalf("codex WriteActive should sync the grafted cred id; got %q", wroteID)
	}
}

func TestRelogin_ActiveSyncError_Propagates(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{ID: "x-syncerr-0001", Name: "e", Provider: "codex", AuthMode: "chatgpt",
		Tokens: &store.CodexTokens{AccessToken: "old-a", RefreshToken: "old-r", AccountID: "acct"}}
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	prevIsActive, prevWrite := codexIsActiveFn, codexWriteActiveFn
	codexIsActiveFn = func(string) bool { return true }
	codexWriteActiveFn = func(*store.Credential) error { return errors.New("disk full") }
	defer func() { codexIsActiveFn, codexWriteActiveFn = prevIsActive, prevWrite }()

	restore := SeamCodexLogin(func(context.Context, io.Writer, io.Reader) (*store.Credential, error) {
		return &store.Credential{ID: "x-syncerr-fresh", Name: "fresh", Provider: "codex", AuthMode: "chatgpt",
			Tokens: &store.CodexTokens{AccessToken: "new-a", RefreshToken: "new-r", AccountID: "acct"}}, nil
	})
	defer restore()

	if _, err := runReloginCmd(t, "x-syncerr-0001"); err == nil {
		t.Fatal("want the active-sync error to propagate")
	}
}

func TestRelogin_Claude_ActiveSyncError_Propagates(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{ID: "c-syncerr-0001", Name: "e",
		ClaudeAiOauth: store.OAuthTokens{AccessToken: "old-a", RefreshToken: "old-r", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}}
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	prevIsActive, prevWrite := claudeIsActiveFn, claudeWriteActiveFn
	claudeIsActiveFn = func(string) bool { return true }
	claudeWriteActiveFn = func(*store.Credential) error { return errors.New("disk full") }
	defer func() { claudeIsActiveFn, claudeWriteActiveFn = prevIsActive, prevWrite }()
	prevBrowser := tryOpenBrowserFn
	tryOpenBrowserFn = func(string) {}
	defer func() { tryOpenBrowserFn = prevBrowser }()

	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example/auth"}
	restore := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(*credflow.Handshake, string) (*store.Credential, error) {
			nc := &store.Credential{ID: "c-syncerr-fresh", Name: "fresh",
				ClaudeAiOauth: store.OAuthTokens{AccessToken: "new-a", RefreshToken: "new-r", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}}
			_ = store.Save(nc)
			return nc, nil
		},
		strings.NewReader("CODE\n"),
	)
	defer restore()

	if _, err := runReloginCmd(t, "c-syncerr-0001"); err == nil {
		t.Fatal("want the claude active-sync error to propagate")
	}
}

// TestRelogin_NilContext exercises the ctx==nil guard: a bare command whose
// Context() was never set returns nil, and runRelogin must fall back to
// context.Background() rather than panic.
func TestRelogin_NilContext(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{ID: "g-nilctx-0001", Name: "n", Provider: "grok",
		GrokTokens: &store.GrokTokens{AccessToken: "old-a", RefreshToken: "old-r"}}
	existing.SetTokens("old-a", "old-r", time.Now().Add(-time.Minute).UnixMilli())
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}
	restore := SeamGrokLogin(func(context.Context, io.Writer, io.Reader) (*store.Credential, error) {
		c := &store.Credential{ID: "g-nilctx-fresh", Name: "f", Provider: "grok",
			GrokTokens: &store.GrokTokens{AccessToken: "new-a", RefreshToken: "new-r"}}
		c.SetTokens("new-a", "new-r", time.Now().Add(time.Hour).UnixMilli())
		return c, nil
	})
	defer restore()

	bare := &cobra.Command{} // Context() is nil until SetContext/ExecuteContext
	bare.SetOut(new(bytes.Buffer))
	if err := runRelogin(bare, []string{"g-nilctx-0001"}); err != nil {
		t.Fatalf("relogin with nil context: %v", err)
	}
}

func TestRelogin_SaveError_Propagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	setupFakeHome(t)
	existing := &store.Credential{ID: "g-saveerr-0001", Name: "s", Provider: "grok",
		GrokTokens: &store.GrokTokens{AccessToken: "old-a", RefreshToken: "old-r"}}
	existing.SetTokens("old-a", "old-r", time.Now().Add(-time.Minute).UnixMilli())
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}
	restore := SeamGrokLogin(func(context.Context, io.Writer, io.Reader) (*store.Credential, error) {
		c := &store.Credential{ID: "g-saveerr-fresh", Name: "f", Provider: "grok",
			GrokTokens: &store.GrokTokens{AccessToken: "new-a", RefreshToken: "new-r"}}
		c.SetTokens("new-a", "new-r", time.Now().Add(time.Hour).UnixMilli())
		return c, nil
	})
	defer restore()

	// Make the store directory read-only so the graft's store.Save fails.
	dir := store.Dir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o700) //nolint:errcheck // restore so t.TempDir cleanup works

	if _, err := runReloginCmd(t, "g-saveerr-0001"); err == nil {
		t.Fatal("want the store.Save error to propagate")
	}
}

func TestReloginShortID(t *testing.T) {
	if got := reloginShortID("abc"); got != "abc" {
		t.Errorf("short id should pass through, got %q", got)
	}
	if got := reloginShortID("0123456789"); got != "01234567" {
		t.Errorf("long id should truncate to 8, got %q", got)
	}
}

func TestRelogin_ResolveError(t *testing.T) {
	setupFakeHome(t)
	if _, err := runReloginCmd(t, "does-not-exist"); err == nil {
		t.Fatal("want error resolving a missing credential")
	}
}

func TestRelogin_LoginError_LeavesExistingUnchanged(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{ID: "g-keep-0001", Name: "keep", Provider: "grok",
		GrokTokens: &store.GrokTokens{AccessToken: "old-a", RefreshToken: "old-r"}}
	existing.SetTokens("old-a", "old-r", time.Now().Add(time.Hour).UnixMilli())
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	restore := SeamGrokLogin(func(context.Context, io.Writer, io.Reader) (*store.Credential, error) {
		return nil, errors.New("auth cancelled")
	})
	defer restore()

	if _, err := runReloginCmd(t, "g-keep-0001"); err == nil {
		t.Fatal("want the login error to propagate")
	}
	got, _ := store.Load("g-keep-0001")
	if got.AccessToken() != "old-a" {
		t.Fatalf("existing tokens must be untouched on login failure, got %q", got.AccessToken())
	}
}

func TestRelogin_Claude_SyncsActiveCopy(t *testing.T) {
	setupFakeHome(t)
	existing := &store.Credential{ID: "c-active-0001", Name: "active",
		ClaudeAiOauth: store.OAuthTokens{AccessToken: "old-a", RefreshToken: "old-r", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}}
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}

	// Force the active branch via the shared cmd seams.
	prevIsActive, prevWrite := claudeIsActiveFn, claudeWriteActiveFn
	var wroteID string
	claudeIsActiveFn = func(id string) bool { return id == "c-active-0001" }
	claudeWriteActiveFn = func(c *store.Credential) error { wroteID = c.ID; return nil }
	defer func() { claudeIsActiveFn, claudeWriteActiveFn = prevIsActive, prevWrite }()

	prevBrowser := tryOpenBrowserFn
	tryOpenBrowserFn = func(string) {}
	defer func() { tryOpenBrowserFn = prevBrowser }()

	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example/auth"}
	restore := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(h *credflow.Handshake, code string) (*store.Credential, error) {
			nc := &store.Credential{ID: "c-active-fresh", Name: "fresh",
				ClaudeAiOauth: store.OAuthTokens{AccessToken: "new-a", RefreshToken: "new-r", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}}
			_ = store.Save(nc)
			return nc, nil
		},
		strings.NewReader("CODE\n"),
	)
	defer restore()

	if _, err := runReloginCmd(t, "c-active-0001"); err != nil {
		t.Fatalf("relogin: %v", err)
	}
	if wroteID != "c-active-0001" {
		t.Fatalf("WriteActive should have synced the grafted cred id; got %q", wroteID)
	}
}
