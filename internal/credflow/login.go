package credflow

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Handshake is the opaque state ferried between BeginLogin and
// CompleteLogin. ID and AuthorizeURL are safe to surface to a client;
// codeVerifier and state stay package-private and are consumed only
// by CompleteLogin's exchange call. The struct is intentionally not
// JSON-serializable: callers needing to ship state across processes
// must persist their own (id → handshake) mapping.
type Handshake struct {
	ID           string
	AuthorizeURL string
	codeVerifier string
	state        string
}

// BeginLogin generates a fresh PKCE pair, builds the authorize URL the
// user must visit to obtain a paste-code, and assigns the handshake an
// opaque 128-bit ID. The caller decides where to stash the returned
// handshake until CompleteLogin is invoked.
func BeginLogin() (*Handshake, error) {
	pkce, err := loginGeneratePKCEFn()
	if err != nil {
		return nil, fmt.Errorf("generate pkce: %w", err)
	}
	id, err := loginNewHandshakeIDFn()
	if err != nil {
		return nil, fmt.Errorf("new handshake id: %w", err)
	}
	return &Handshake{
		ID:           id,
		AuthorizeURL: oauth.BuildAuthorizeURL(pkce),
		codeVerifier: pkce.CodeVerifier,
		state:        pkce.State,
	}, nil
}

// CompleteLogin exchanges code (the paste-code the user obtained
// after visiting Handshake.AuthorizeURL) for an OAuth token pair,
// fetches the profile to derive the credential's name + tier, and
// persists a fresh store.Credential. The saved credential is returned
// so callers can render the new row immediately.
func CompleteLogin(h *Handshake, code string) (*store.Credential, error) {
	pkce := &oauth.PKCEParams{CodeVerifier: h.codeVerifier, State: h.state}
	tokens, err := loginExchangeFn(code, h.state, pkce)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	scopes := strings.Fields(tokens.Scope)
	if len(scopes) == 0 {
		scopes = oauth.Scopes
	}

	profile := loginFetchProfileFn(tokens.AccessToken)
	name := profile.Email
	if name == "" {
		name = id
	}

	cred := &store.Credential{
		ID:   id,
		Name: name,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  tokens.AccessToken,
			RefreshToken: tokens.RefreshToken,
			ExpiresAt:    time.Now().UnixMilli() + tokens.ExpiresIn*1000,
			Scopes:       scopes,
		},
		Subscription:    store.Subscription{Tier: profile.Tier},
		CreatedAt:       now,
		LastRefreshedAt: now,
	}

	if err := loginSaveFn(cred); err != nil {
		return nil, fmt.Errorf("save credentials: %w", err)
	}
	return cred, nil
}

// newHandshakeID returns a 128-bit base64url string suitable as an
// opaque map key for in-flight login handshakes.
func newHandshakeID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// coverage: unreachable — crypto/rand only errors on a kernel
		// RNG failure, which is not exercisable in tests.
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Package-level seams. Consumers outside this package never touch
// them; tests swap them to avoid real HTTP round-trips and to prove
// the error branches.
var (
	loginGeneratePKCEFn   = oauth.GeneratePKCE
	loginNewHandshakeIDFn = newHandshakeID
	loginExchangeFn       = oauth.ExchangeCode
	loginFetchProfileFn   = oauth.FetchProfile
	loginSaveFn           = store.Save
)
