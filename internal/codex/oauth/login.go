package codexoauth

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Login runs the paste-URL login flow:
//  1. Print authorize URL + instructions to stdout.
//  2. Read the pasted redirect URL from stdin.
//  3. Validate state; extract code; exchange for tokens.
//
// Returns a fresh codex Credential (not yet saved to disk).
func Login(ctx context.Context, stdout io.Writer, stdin io.Reader) (*store.Credential, error) {
	pkce, err := GeneratePKCE()
	if err != nil { // untestable: crypto/rand.Read panics before returning err (Go 1.20+)
		return nil, fmt.Errorf("codexoauth: generate PKCE: %w", err)
	}

	redirectURI := DefaultRedirectURI
	authURL := BuildAuthorizeURL(pkce, redirectURI)

	fmt.Fprintln(stdout, "Open this URL in your browser to authorize ccm with OpenAI:")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  "+authURL)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "After authorizing, your browser will redirect to a localhost URL.")
	fmt.Fprintln(stdout, "The page won't load (ccm doesn't run a local server) — that's expected.")
	fmt.Fprintln(stdout, "Copy the FULL URL from your browser's address bar and paste it here:")
	fmt.Fprint(stdout, "> ")

	br := bufio.NewReader(stdin)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return nil, fmt.Errorf("codexoauth: read pasted URL: %w", err)
	}

	code, state, err := parseCallbackURL(line)
	if err != nil {
		return nil, err
	}
	if state != pkce.State {
		return nil, ErrStateMismatch
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

// parseCallbackURL extracts code+state from the redirect URL the user
// pastes. Recognizes OAuth error params and returns typed errors.
func parseCallbackURL(input string) (code, state string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("codexoauth: pasted URL is empty")
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", "", fmt.Errorf("codexoauth: parse pasted URL: %w", err)
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return "", "", ErrAuthDenied
		}
		desc := q.Get("error_description")
		if desc == "" {
			desc = e
		}
		return "", "", fmt.Errorf("%w: %s", ErrTokenEndpoint, desc)
	}
	code = q.Get("code")
	state = q.Get("state")
	if code == "" {
		return "", "", fmt.Errorf("codexoauth: pasted URL has no code parameter")
	}
	return code, state, nil
}

// ExportedParseCallbackURL exposes parseCallbackURL for package-external tests.
var ExportedParseCallbackURL = parseCallbackURL
