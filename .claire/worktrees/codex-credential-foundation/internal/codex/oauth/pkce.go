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

type PKCEParams struct {
	Verifier  string
	Challenge string
	State     string
}

// GeneratePKCE returns a fresh PKCEParams. Verifier is 64 URL-safe
// chars (within RFC 7636's [43,128] range); Challenge is its S256 hash.
// State is 32 URL-safe chars.
func GeneratePKCE() (*PKCEParams, error) {
	verifier, err := randomURLSafe(48)
	if err != nil {
		return nil, err
	}
	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return &PKCEParams{Verifier: verifier, Challenge: challenge, State: state}, nil
}

func randomURLSafe(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
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
