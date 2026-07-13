// Package grokoauth implements OAuth 2.0 PKCE for xAI Grok (SuperGrok /
// X Premium+ subscription). ccm does not listen on the redirect port;
// the user copies the full redirect URL from their browser's address bar,
// mirroring the codex login UX.
package grokoauth

import "errors"

var (
	ErrStateMismatch  = errors.New("grokoauth: oauth state mismatch")
	ErrAuthDenied     = errors.New("grokoauth: user denied authorization")
	ErrRefreshRotated = errors.New("grokoauth: refresh token has been invalidated")
	ErrTokenEndpoint  = errors.New("grokoauth: token endpoint returned non-2xx")
)
