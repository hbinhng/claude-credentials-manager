package codexoauth

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// LoginTimeout is the wall-clock budget for the entire login flow.
var LoginTimeout = 5 * time.Minute

// OpenBrowser opens u in the user's default browser. Tests override.
var OpenBrowser = OpenBrowserDefault

// LoginContext is published once during Login() so tests can drive
// the callback synchronously without sleeps.
type LoginContext struct {
	BindAddr string
	State    string
}

// SeamLoginContext is invoked once after the callback server is bound
// and the PKCE state is generated. Production no-op; tests override.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
var SeamLoginContext = func(LoginContext) {}

// Login runs the full PKCE → browser → callback → token-exchange flow
// and returns a fresh codex Credential. Persisting to disk is the
// caller's responsibility.
func Login(ctx context.Context) (*store.Credential, error) {
	pkce, err := GeneratePKCE()
	if err != nil { // untestable: crypto/rand.Read panics before returning err (Go 1.20+)
		return nil, fmt.Errorf("codexoauth: generate PKCE: %w", err)
	}

	srv, addr, err := StartCallbackServer(pkce.State)
	if err != nil {
		return nil, err
	}
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	SeamLoginContext(LoginContext{BindAddr: addr, State: pkce.State})

	redirectURI := DefaultRedirectURI
	if ListenAddr != DefaultListenAddr {
		redirectURI = "http://" + addr + "/auth/callback"
	}
	authURL := BuildAuthorizeURL(pkce, redirectURI)

	_ = OpenBrowser(authURL) // browser failure is non-fatal

	code, err := srv.Wait(LoginTimeout)
	if err != nil {
		return nil, err
	}

	tr, err := ExchangeCode(code, pkce.Verifier, redirectURI)
	if err != nil {
		return nil, err
	}

	claims, _ := ParseClaims(tr.IDToken)
	name := claims.Email
	if name == "" {
		id := uuid.NewString()
		if len(id) > 8 {
			id = id[:8]
		}
		name = id
	}
	now := time.Now().UTC()
	return &store.Credential{
		ID:              uuid.NewString(),
		Name:            name,
		Provider:        "codex",
		CreatedAt:       now.Format(time.RFC3339),
		LastRefreshedAt: now.Format(time.RFC3339),
		AuthMode:        "chatgpt",
		OpenAIAPIKey:    nil,
		Tokens: &store.CodexTokens{
			IDToken:      tr.IDToken,
			AccessToken:  tr.AccessToken,
			RefreshToken: tr.RefreshToken,
			AccountID:    claims.AccountID,
		},
		LastRefresh: now.Format(time.RFC3339Nano),
	}, nil
}
