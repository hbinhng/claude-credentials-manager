package cmd

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// saveCodexCred builds and stores a codex credential for launch/share guard tests.
func saveCodexCred(t *testing.T, id, name string) *store.Credential {
	t.Helper()
	exp := time.Now().Add(time.Hour).Unix()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(
		`{"email":"u@x.com","exp":` + strconv.FormatInt(exp, 10) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`,
	))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	tok := h + "." + p + "." + s
	cred := &store.Credential{
		ID: id, Name: name, Provider: "codex",
		AuthMode: "chatgpt", OpenAIAPIKey: nil,
		Tokens:          &store.CodexTokens{IDToken: tok, AccessToken: tok, RefreshToken: "rt_a.b", AccountID: "acct"},
		LastRefresh:     "2026-05-08T00:00:00Z",
		CreatedAt:       "2026-05-08T00:00:00Z",
		LastRefreshedAt: "2026-05-08T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save codex cred: %v", err)
	}
	return cred
}

// TestLaunchCommand_ModelAliasConflictRejected verifies that conflicting
// --model-alias patterns (overlapping source globs) cause runLaunchLocal
// to return an error at parse time, before any subprocess is spawned.
func TestLaunchCommand_ModelAliasConflictRejected(t *testing.T) {
	// Override the package-level flag var for this test only.
	orig := launchModelAliases
	launchModelAliases = []string{"claude-*=gpt-5", "claude-opus-*=gpt-4"}
	t.Cleanup(func() { launchModelAliases = orig })

	setupHomeWithCcm(t)
	saveCodexCred(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaab", "codex-alias-test")
	// Use a claude credential — the alias parse check fires before the
	// codex-CLI check, so provider does not matter.
	cred := &store.Credential{
		ID:   "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
		Name: "claude-alias-test",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "at",
			RefreshToken: "rt",
			ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		},
		CreatedAt:       "2026-01-01T00:00:00Z",
		LastRefreshedAt: "2026-01-01T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	err := runLaunchLocal(cred.ID, nil)
	if err == nil {
		t.Fatal("runLaunchLocal: nil err, want error for conflicting --model-alias")
	}
	if !strings.Contains(err.Error(), "parse --model-alias") {
		t.Errorf("err = %v; want 'parse --model-alias' in message", err)
	}
}

// TestLaunchPreflightRefresh_UsesCredflow verifies that the pre-flight
// refresh in runLaunchLocal routes through credflow.RefreshFn (provider-
// aware, file-locked) rather than calling oauth.Refresh directly. A
// claude credential with an expired access token is used so the refresh
// path is entered.
func TestLaunchPreflightRefresh_UsesCredflow(t *testing.T) {
	setupHomeWithCcm(t)

	// Build a claude credential that is already expired so IsExpired()
	// returns true and the pre-flight block is entered.
	credID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	expiredAt := time.Now().Add(-1 * time.Hour).UnixMilli()
	cred := &store.Credential{
		ID:   credID,
		Name: "expiring-claude",
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			ExpiresAt:    expiredAt,
			Scopes:       []string{"user:inference"},
		},
		Subscription:    store.Subscription{Tier: "Claude Pro"},
		CreatedAt:       "2026-01-01T00:00:00Z",
		LastRefreshedAt: "2026-01-01T00:00:00Z",
	}
	if err := store.Save(cred); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	called := false
	restore := credflow.SetRefreshFnForTest(func(id string) (*store.Credential, error) {
		called = true
		if id != credID {
			t.Errorf("credflow.RefreshFn called with id=%q, want %q", id, credID)
		}
		// Return an updated credential so runLaunchLocal continues with
		// a fresh token (it will still fail at share.NewLocalProxy, which
		// is fine — we only need to confirm the seam was hit).
		refreshed := *cred
		refreshed.ClaudeAiOauth.AccessToken = "new-access"
		refreshed.ClaudeAiOauth.ExpiresAt = time.Now().Add(1 * time.Hour).UnixMilli()
		return &refreshed, nil
	})
	defer restore()

	// Stub the exec seam so runLaunchLocal does not spawn the real claude
	// binary (which would call os.Exit and kill the test process).
	restoreExec := share.SetLaunchExecFnForTest(func(string, []string, []string) error {
		return errors.New("stub: no claude in test")
	})
	defer restoreExec()

	// runLaunchLocal will error (stub exec returns error) — that's expected.
	// We only care that called is true, proving the pre-flight block went
	// through credflow rather than the old direct oauth.Refresh call.
	_ = runLaunchLocal(credID, nil)

	if !called {
		t.Error("credflow.RefreshFn was not called; launch still uses inline oauth.Refresh")
	}
}
