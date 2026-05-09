package identity_test

import (
	"net/http"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/identity"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func newReq(t *testing.T) *http.Request {
	t.Helper()
	req, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header = http.Header{}
	return req
}

func TestBundle_ApplySetsStaticHeaders(t *testing.T) {
	cred := &store.Credential{
		Provider: "codex",
		Tokens:   &store.CodexTokens{AccessToken: "fresh-token", AccountID: "acc-1"},
	}
	b := identity.New(cred)
	req := newReq(t)
	b.Apply(req)

	cases := []struct {
		key  string
		want string
	}{
		{"Authorization", "Bearer fresh-token"},
		{"Version", identity.StaticVersion},
		{"Openai-Beta", identity.StaticOpenaiBeta},
		{"User-Agent", identity.StaticUserAgent},
		{"Accept", "text/event-stream"},
		{"Content-Type", "application/json"},
		{"chatgpt-account-id", "acc-1"},
	}
	for _, c := range cases {
		if got := req.Header.Get(c.key); got != c.want {
			t.Errorf("%s = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestBundle_ApplyNoAccountID(t *testing.T) {
	cred := &store.Credential{
		Provider: "codex",
		Tokens:   &store.CodexTokens{AccessToken: "tok"},
	}
	b := identity.New(cred)
	req := newReq(t)
	b.Apply(req)

	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "" {
		t.Errorf("chatgpt-account-id should be absent when AccountID is empty, got %q", got)
	}
}

func TestBundle_ApplyNilBundle(t *testing.T) {
	var b *identity.Bundle
	req := newReq(t)
	b.Apply(req) // must not panic
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("nil Bundle.Apply should set nothing, got Authorization=%q", got)
	}
}

func TestBundle_ApplyNilCred(t *testing.T) {
	b := identity.New(nil)
	req := newReq(t)
	b.Apply(req) // must not panic
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("nil cred Bundle.Apply should set nothing, got Authorization=%q", got)
	}
}

func TestBundle_ApplyNilTokens(t *testing.T) {
	cred := &store.Credential{Provider: "codex"}
	b := identity.New(cred)
	req := newReq(t)
	b.Apply(req)
	// With nil Tokens, cred.AccessToken() returns "" (no panic), so
	// Authorization is still written as "Bearer " and the static
	// headers still flow. Only chatgpt-account-id is suppressed.
	if got := req.Header.Get("Authorization"); got != "Bearer " {
		t.Errorf("Authorization = %q, want %q", got, "Bearer ")
	}
	if got := req.Header.Get("Version"); got != identity.StaticVersion {
		t.Errorf("Version = %q, want %q", got, identity.StaticVersion)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "" {
		t.Errorf("chatgpt-account-id should be absent when Tokens is nil, got %q", got)
	}
}
