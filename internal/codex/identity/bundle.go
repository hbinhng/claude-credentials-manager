// Package identity assembles per-request outbound headers for codex
// requests by combining the captured codex-CLI header bundle with the
// credential's current bearer token.
package identity

import (
	"net/http"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Bundle holds the captured header set from a per-session codex CLI run.
// Apply writes those headers onto an outbound request and overrides
// Authorization with the credential's current bearer.
type Bundle struct {
	captured http.Header
}

// New constructs a Bundle from a captured header set. nil is permitted
// (Apply still sets Authorization + chatgpt-account-id from the cred).
func New(captured http.Header) *Bundle {
	return &Bundle{captured: captured}
}

// Apply writes b's captured headers onto req, then overrides
// Authorization with cred.AccessToken() and sets chatgpt-account-id from
// cred.Tokens.AccountID. Hop-by-hop headers from the captured set are
// not filtered here — capture only records request headers, never
// hop-by-hop.
func (b *Bundle) Apply(req *http.Request, cred *store.Credential) {
	if b != nil {
		for name, values := range b.captured {
			req.Header[name] = append([]string{}, values...)
		}
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken())
	if cred.Tokens != nil && cred.Tokens.AccountID != "" {
		req.Header.Set("chatgpt-account-id", cred.Tokens.AccountID)
	}
}
