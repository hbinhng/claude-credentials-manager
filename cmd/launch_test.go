package cmd

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestLaunchCommand_RejectsCodexCred(t *testing.T) {
	setupHomeWithCcm(t)
	saveCodexCred(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "codex-test")

	err := runLaunchLocal("codex-test", nil)
	if err == nil {
		t.Fatal("runLaunchLocal: nil err, want rejection for codex cred")
	}
	if !strings.Contains(err.Error(), "claude-only") {
		t.Errorf("err = %v; want 'claude-only' in message", err)
	}
}
