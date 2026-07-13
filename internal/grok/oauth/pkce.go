package grokoauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
)

// OAuth client constants, reverse-engineered from the open-source xAI OAuth
// plugins. Both plugins agree verbatim on the client_id, issuer, redirect,
// and scopes below. To be confirmed against a live login in the Task 6 spike.
// AuthorizeURL is a var so tests can point it at an httptest server.
//
// SOURCE: https://raw.githubusercontent.com/ysnock404/opencode-grok-auth/master/src/constants.ts
// SOURCE: https://raw.githubusercontent.com/BlockedPath/pi-xai-oauth/main/extensions/xai/constants.ts
const ClientID = "b1a00492-073a-47ea-816f-4c329264a828"

// AuthorizeURL is the xAI OIDC authorization endpoint. opencode-grok-auth
// hardcodes this exact path; pi-xai-oauth resolves it from OIDC discovery on
// issuer https://auth.x.ai, which yields the same endpoint.
// SOURCE: https://raw.githubusercontent.com/ysnock404/opencode-grok-auth/master/src/constants.ts
var AuthorizeURL = "https://auth.x.ai/oauth2/authorize"

// Scopes is the space-delimited scope set both plugins request verbatim.
// SOURCE: https://raw.githubusercontent.com/BlockedPath/pi-xai-oauth/main/extensions/xai/constants.ts
const Scopes = "openid profile email offline_access grok-cli:access api:access"

// DefaultRedirectURI is the loopback callback both plugins register (host
// 127.0.0.1, port 56121, path /callback). ccm does NOT listen on it — the
// user pastes the full redirect URL from the browser after the page fails
// to load, mirroring the codex login UX.
// SOURCE: https://raw.githubusercontent.com/BlockedPath/pi-xai-oauth/main/extensions/xai/constants.ts
const DefaultRedirectURI = "http://127.0.0.1:56121/callback"

type PKCEParams struct {
	Verifier  string
	Challenge string
	State     string
}

// GeneratePKCE returns fresh PKCE params. Verifier is 64 URL-safe chars
// (within RFC 7636's [43,128]); Challenge is its S256 hash; State is 32
// URL-safe chars.
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

// BuildAuthorizeURL composes the authorize URL with the PKCE + redirect
// params. The two source plugins disagree on extra params: opencode-grok-auth
// adds plan=generic&referrer=hermes-agent, while pi-xai-oauth deliberately
// omits them with an inline comment warning they change xAI's routing and
// push users toward the API-console SSO surface instead of the Grok consent
// screen. We follow the safer pi-xai-oauth approach (no extra params); the
// Task 6 spike confirms against a live login and adds any that prove required.
func BuildAuthorizeURL(p *PKCEParams, redirectURI string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", Scopes)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", p.State)
	return AuthorizeURL + "?" + q.Encode()
}
