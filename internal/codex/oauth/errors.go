// Package codexoauth implements OAuth 2.0 PKCE for Codex/ChatGPT.
//
// Codex uses the same OpenAI public OAuth client as the codex CLI
// (client_id app_EMoamEEZ73f0CkXaXp7hrann) with redirect_uri
// http://localhost:1455/auth/callback and the rotating-refresh-token
// flavor of refresh_token grant.
package codexoauth

import "errors"

var (
	ErrPortInUse       = errors.New("codexoauth: port 1455 in use")
	ErrCallbackTimeout = errors.New("codexoauth: login callback timed out")
	ErrStateMismatch   = errors.New("codexoauth: oauth state mismatch")
	ErrAuthDenied      = errors.New("codexoauth: user denied authorization")
	ErrRefreshRotated  = errors.New("codexoauth: refresh token has been invalidated")
	ErrTokenEndpoint   = errors.New("codexoauth: token endpoint returned non-2xx")
)
