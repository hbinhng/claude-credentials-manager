package store

import "testing"

func TestCredential_AccessToken_Claude(t *testing.T) {
	c := &Credential{
		Provider:      "claude",
		ClaudeAiOauth: OAuthTokens{AccessToken: "claude-bearer", RefreshToken: "claude-rt", ExpiresAt: 12345},
	}
	if got := c.AccessToken(); got != "claude-bearer" {
		t.Errorf("AccessToken = %q, want claude-bearer", got)
	}
	if got := c.RefreshToken(); got != "claude-rt" {
		t.Errorf("RefreshToken = %q, want claude-rt", got)
	}
	if got := c.ExpiresAtMillis(); got != 12345 {
		t.Errorf("ExpiresAtMillis = %d, want 12345", got)
	}
}

func TestCredential_AccessToken_Codex(t *testing.T) {
	c := &Credential{
		Provider: "codex",
		Tokens:   &CodexTokens{AccessToken: "codex-bearer", RefreshToken: "codex-rt", AccountID: "acc-1"},
	}
	c.expiresAtMillis = 67890 // codex creds derive expiry from JWT at unmarshal time
	if got := c.AccessToken(); got != "codex-bearer" {
		t.Errorf("AccessToken = %q, want codex-bearer", got)
	}
	if got := c.RefreshToken(); got != "codex-rt" {
		t.Errorf("RefreshToken = %q, want codex-rt", got)
	}
	if got := c.ExpiresAtMillis(); got != 67890 {
		t.Errorf("ExpiresAtMillis = %d, want 67890", got)
	}
}

func TestCredential_AccessToken_CodexNilTokens(t *testing.T) {
	c := &Credential{Provider: "codex", Tokens: nil}
	if got := c.AccessToken(); got != "" {
		t.Errorf("AccessToken with nil Tokens = %q, want empty", got)
	}
	if got := c.RefreshToken(); got != "" {
		t.Errorf("RefreshToken with nil Tokens = %q, want empty", got)
	}
}

func TestCredential_SetTokens_Claude(t *testing.T) {
	c := &Credential{Provider: "claude"}
	c.SetTokens("new-access", "new-refresh", 99999)
	if c.ClaudeAiOauth.AccessToken != "new-access" {
		t.Errorf("ClaudeAiOauth.AccessToken = %q", c.ClaudeAiOauth.AccessToken)
	}
	if c.ClaudeAiOauth.RefreshToken != "new-refresh" {
		t.Errorf("ClaudeAiOauth.RefreshToken = %q", c.ClaudeAiOauth.RefreshToken)
	}
	if c.ClaudeAiOauth.ExpiresAt != 99999 {
		t.Errorf("ClaudeAiOauth.ExpiresAt = %d", c.ClaudeAiOauth.ExpiresAt)
	}
	if c.expiresAtMillis != 99999 {
		t.Errorf("expiresAtMillis cache = %d", c.expiresAtMillis)
	}
}

func TestCredential_SetTokens_Codex(t *testing.T) {
	c := &Credential{Provider: "codex", Tokens: &CodexTokens{AccountID: "acc-keep"}}
	c.SetTokens("new-access", "new-refresh", 88888)
	if c.Tokens.AccessToken != "new-access" {
		t.Errorf("Tokens.AccessToken = %q", c.Tokens.AccessToken)
	}
	if c.Tokens.RefreshToken != "new-refresh" {
		t.Errorf("Tokens.RefreshToken = %q", c.Tokens.RefreshToken)
	}
	if c.Tokens.AccountID != "acc-keep" {
		t.Errorf("AccountID got clobbered: %q", c.Tokens.AccountID)
	}
	if c.expiresAtMillis != 88888 {
		t.Errorf("expiresAtMillis cache = %d", c.expiresAtMillis)
	}
}

func TestCredential_SetTokens_CodexNilTokens(t *testing.T) {
	c := &Credential{Provider: "codex", Tokens: nil}
	c.SetTokens("a", "r", 1)
	if c.Tokens == nil {
		t.Fatal("Tokens still nil after SetTokens")
	}
	if c.Tokens.AccessToken != "a" {
		t.Errorf("AccessToken = %q", c.Tokens.AccessToken)
	}
}
