package oauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// BuildAuthorizeURL renders the OAuth authorize URL the user must
// visit to obtain the paste-code. Exported so credflow.BeginLogin can
// hand the URL to a web client without re-implementing the same
// query-string assembly.
func BuildAuthorizeURL(pkce *PKCEParams) string {
	params := url.Values{
		"code":                  {"true"},
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {strings.Join(Scopes, " ")},
		"code_challenge":        {pkce.CodeChallenge},
		"code_challenge_method": {CodeChallengeMethod},
		"state":                 {pkce.State},
	}
	return AuthorizeURL + "?" + params.Encode()
}

// ExchangeCode swaps an OAuth authorization code (with optional
// "#state" suffix) plus the original PKCE verifier for an access +
// refresh token pair. Exported so credflow.CompleteLogin can drive
// the exchange without going through the CLI's stdin prompt.
func ExchangeCode(code, state string, pkce *PKCEParams) (*TokenResponse, error) {
	// The auth code may contain #state appended
	authCode := code
	codeState := ""
	if idx := strings.Index(authCode, "#"); idx >= 0 {
		codeState = authCode[idx+1:]
		authCode = authCode[:idx]
	}
	if codeState == "" {
		codeState = state
	}

	payload := map[string]string{
		"code":          authCode,
		"state":         codeState,
		"grant_type":    "authorization_code",
		"client_id":     ClientID,
		"redirect_uri":  RedirectURI,
		"code_verifier": pkce.CodeVerifier,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", TokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: httpx.Transport(), Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(respBody, &tokens); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	return &tokens, nil
}
