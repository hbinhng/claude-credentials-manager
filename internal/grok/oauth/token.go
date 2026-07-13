package grokoauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenURL is xAI's OAuth token endpoint. Var so tests override.
// SOURCE: https://auth.x.ai (OIDC discovery issuer, aligns with AuthorizeURL in pkce.go)
var TokenURL = "https://auth.x.ai/oauth2/token"

// TokenResponse is the subset of fields ccm uses.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// Refresh swaps a rotating refresh token for a new triple. xAI rotates
// refresh tokens on every use — the new refresh_token REPLACES the old.
func Refresh(refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", ClientID)
	return postTokenForm(form)
}

// ExchangeCode swaps an authorization_code for the initial token triple.
func ExchangeCode(code, codeVerifier, redirectURI string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", ClientID)
	form.Set("code_verifier", codeVerifier)
	return postTokenForm(form)
}

func postTokenForm(form url.Values) (*TokenResponse, error) {
	req, err := http.NewRequest("POST", TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		// TokenURL is a package var; a malformed override (see TestExchangeCode_InvalidTokenURL) reaches this branch.
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grokoauth: post token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode == http.StatusOK {
		var tr TokenResponse
		if err := json.Unmarshal(body, &tr); err != nil {
			return nil, fmt.Errorf("grokoauth: parse token response: %w", err)
		}
		return &tr, nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		lower := strings.ToLower(string(body))
		if strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "invalid_request") && strings.Contains(lower, "refresh") {
			return nil, fmt.Errorf("%w: %s", ErrRefreshRotated, string(body))
		}
	}
	return nil, fmt.Errorf("%w: status=%d body=%s", ErrTokenEndpoint, resp.StatusCode, string(body))
}
