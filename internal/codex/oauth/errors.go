// Package codexoauth implements OAuth 2.0 PKCE for Codex/ChatGPT.
//
// Codex uses the same OpenAI public OAuth client as the codex CLI
// (client_id app_EMoamEEZ73f0CkXaXp7hrann) with redirect_uri
// http://localhost:1455/auth/callback. ccm does not listen on that port;
// the user copies the full redirect URL from their browser's address bar.
package codexoauth

import "errors"

var (
	ErrStateMismatch  = errors.New("codexoauth: oauth state mismatch")
	ErrAuthDenied     = errors.New("codexoauth: user denied authorization")
	ErrRefreshRotated = errors.New("codexoauth: refresh token has been invalidated")
	ErrTokenEndpoint  = errors.New("codexoauth: token endpoint returned non-2xx")
)
