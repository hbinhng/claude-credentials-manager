package codexoauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
)

const ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const AuthorizeURL = "https://auth.openai.com/oauth/authorize"
const Scopes = "openid profile email offline_access api.connectors.read api.connectors.invoke"

// DefaultRedirectURI matches what's registered on the OpenAI OAuth app.
// ccm does not listen on this port — the user copies the full redirect
// URL from their browser's address bar after the browser fails to load it.
const DefaultRedirectURI = "http://localhost:1455/auth/callback"

type PKCEParams struct {
	Verifier  string
	Challenge string
	State     string
}

// GeneratePKCE returns a fresh PKCEParams. Verifier is 64 URL-safe
// chars (within RFC 7636's [43,128] range); Challenge is its S256 hash.
// State is 32 URL-safe chars.
//
// GeneratePKCE never returns a non-nil error: Go 1.20+ made crypto/rand.Read
// fatal on OS failure, so the error path is unreachable in practice.
func GeneratePKCE() (*PKCEParams, error) {
	verifier := randomURLSafe(48)
	state := randomURLSafe(24)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return &PKCEParams{Verifier: verifier, Challenge: challenge, State: state}, nil
}

// randomURLSafe returns nBytes of cryptographically random data encoded as
// base64url without padding. crypto/rand.Read panics on OS failure (Go 1.20+),
// so no error is possible.
func randomURLSafe(nBytes int) string {
	buf := make([]byte, nBytes)
	rand.Read(buf) //nolint:errcheck // crypto/rand.Read panics on error since Go 1.20
	return base64.RawURLEncoding.EncodeToString(buf)
}

// BuildAuthorizeURL composes the full authorize URL with the exact
// param set codex CLI 0.129.0 sends (verified against captured wire URL).
func BuildAuthorizeURL(p *PKCEParams, redirectURI string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", Scopes)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", p.State)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", "codex_cli_rs")
	return AuthorizeURL + "?" + q.Encode()
}
