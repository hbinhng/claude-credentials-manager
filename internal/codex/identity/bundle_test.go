package identity_test

import (
	"net/http"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/identity"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestBundle_ApplyOverwritesAuth(t *testing.T) {
	captured := http.Header{}
	captured.Set("Authorization", "Bearer captured-stale-bearer")
	captured.Set("originator", "codex_cli_rs")
	captured.Set("session_id", "sess-abc")
	captured.Set("User-Agent", "codex-cli/0.129.0")

	b := identity.New(captured)
	cred := &store.Credential{Provider: "codex", Tokens: &store.CodexTokens{AccessToken: "fresh-token", AccountID: "acc-1"}}

	req, _ := http.NewRequest("POST", "https://chatgpt.com/v1/responses", nil)
	b.Apply(req, cred)

	if got := req.Header.Get("Authorization"); got != "Bearer fresh-token" {
		t.Errorf("Authorization = %q, want Bearer fresh-token", got)
	}
	if got := req.Header.Get("originator"); got != "codex_cli_rs" {
		t.Errorf("originator = %q", got)
	}
	if got := req.Header.Get("session_id"); got != "sess-abc" {
		t.Errorf("session_id = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "codex-cli/0.129.0" {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestBundle_ApplyAddsAccountID(t *testing.T) {
	captured := http.Header{}
	b := identity.New(captured)
	cred := &store.Credential{Provider: "codex", Tokens: &store.CodexTokens{AccessToken: "tok", AccountID: "acc-X"}}

	req, _ := http.NewRequest("POST", "https://chatgpt.com/v1/responses", nil)
	b.Apply(req, cred)

	if got := req.Header.Get("chatgpt-account-id"); got != "acc-X" {
		t.Errorf("chatgpt-account-id = %q, want acc-X", got)
	}
}

func TestBundle_ApplyNilCaptured(t *testing.T) {
	b := identity.New(nil)
	cred := &store.Credential{Provider: "codex", Tokens: &store.CodexTokens{AccessToken: "tok"}}
	req, _ := http.NewRequest("POST", "https://chatgpt.com/v1/responses", nil)
	b.Apply(req, cred)
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestBundle_ApplyNilBundle(t *testing.T) {
	var b *identity.Bundle
	cred := &store.Credential{Provider: "codex", Tokens: &store.CodexTokens{AccessToken: "tok", AccountID: "acc-123"}}
	req, _ := http.NewRequest("POST", "https://chatgpt.com/v1/responses", nil)
	b.Apply(req, cred)
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acc-123" {
		t.Errorf("chatgpt-account-id = %q, want acc-123", got)
	}
}
