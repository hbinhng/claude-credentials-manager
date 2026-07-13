package grokoauth

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Login runs the paste-URL PKCE flow and returns a fresh grok Credential
// (not yet saved). ctx is accepted for signature parity with codex Login
// and future cancellation; the single stdin read is not yet ctx-aware.
func Login(ctx context.Context, stdout io.Writer, stdin io.Reader) (*store.Credential, error) {
	_ = ctx
	pkce, err := GeneratePKCE()
	if err != nil { // untestable: crypto/rand.Read panics before returning err
		return nil, fmt.Errorf("grokoauth: generate PKCE: %w", err)
	}

	redirectURI := DefaultRedirectURI
	authURL := BuildAuthorizeURL(pkce, redirectURI)

	fmt.Fprintln(stdout, "Open this URL in your browser to authorize ccm with xAI (Grok):")
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
		return nil, fmt.Errorf("grokoauth: read pasted URL: %w", err)
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

	name := claimEmail(tr.IDToken)
	if name == "" {
		id := uuid.NewString()
		if len(id) > 8 {
			id = id[:8]
		}
		name = id
	}

	now := time.Now().UTC()
	cred := &store.Credential{
		ID:              uuid.NewString(),
		Name:            name,
		Provider:        "grok",
		CreatedAt:       now.Format(time.RFC3339),
		LastRefreshedAt: now.Format(time.RFC3339),
		GrokTokens:      &store.GrokTokens{},
		LastRefresh:     now.Format(time.RFC3339Nano),
	}
	cred.SetTokens(tr.AccessToken, tr.RefreshToken, now.UnixMilli()+int64(tr.ExpiresIn)*1000)
	return cred, nil
}

// claimEmail best-effort extracts the "email" claim from an unsigned JWT
// id_token. Returns "" when absent or unparseable (no signature check).
func claimEmail(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	// Reuse store's exported-free base64 by decoding here.
	// Minimal: decode payload, look for "email".
	payload, err := b64URLDecode(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := jsonUnmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Email
}

func parseCallbackURL(input string) (code, state string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("grokoauth: pasted URL is empty")
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", "", fmt.Errorf("grokoauth: parse pasted URL: %w", err)
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
		return "", "", fmt.Errorf("grokoauth: pasted URL has no code parameter")
	}
	return code, state, nil
}

// ExportedParseCallbackURL exposes parseCallbackURL for external tests.
var ExportedParseCallbackURL = parseCallbackURL

func b64URLDecode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
func jsonUnmarshal(b []byte, v any) error   { return json.Unmarshal(b, v) }
