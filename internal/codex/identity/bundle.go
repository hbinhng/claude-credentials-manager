package identity

import (
	"net/http"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Bundle synthesizes per-request outbound headers from compile-time
// constants and the credential's runtime metadata. Replaces the
// captured-headers model from sub-project B per spec
// 2026-05-09-codex-omniroute-pivot-design §5.1.
type Bundle struct {
	cred *store.Credential
}

// New constructs a Bundle for the given credential. nil cred is
// permitted (Apply becomes a no-op).
func New(cred *store.Credential) *Bundle {
	return &Bundle{cred: cred}
}

// Apply writes the synthesized identity headers onto req.
// Authorization tracks cred.AccessToken() so mid-session refresh
// propagates on each request build. chatgpt-account-id is set from
// cred.Tokens.AccountID when present and skipped otherwise.
//
// Headers set:
//
//	Authorization      = "Bearer " + cred.AccessToken()
//	chatgpt-account-id = cred.Tokens.AccountID (if non-empty)
//	Version            = StaticVersion
//	Openai-Beta        = StaticOpenaiBeta
//	User-Agent         = StaticUserAgent
//	Accept             = "text/event-stream"
//	Content-Type       = "application/json"
//
// Apply is a no-op when b is nil or b.cred is nil.
func (b *Bundle) Apply(req *http.Request) {
	if b == nil || b.cred == nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+b.cred.AccessToken())
	req.Header.Set("Version", StaticVersion)
	req.Header.Set("Openai-Beta", StaticOpenaiBeta)
	req.Header.Set("User-Agent", StaticUserAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if b.cred.Tokens != nil && b.cred.Tokens.AccountID != "" {
		req.Header.Set("chatgpt-account-id", b.cred.Tokens.AccountID)
	}
}
