package codexoauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenURL is OpenAI's OAuth token endpoint. Tests override.
var TokenURL = "https://auth.openai.com/oauth/token"

// TokenResponse is the subset of fields ccm uses.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// Refresh swaps a rotating refresh token for a new (access, refresh, id)
// triple. The new refresh_token REPLACES the old one (one-time-use).
func Refresh(refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", ClientID)
	form.Set("scope", Scopes)
	return postTokenForm(form)
}

// ExchangeCode swaps an authorization_code for an initial token triple.
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
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codexoauth: post token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	bodyStr := Redact(string(body))

	if resp.StatusCode == http.StatusOK {
		var tr TokenResponse
		if err := json.Unmarshal(body, &tr); err != nil {
			return nil, fmt.Errorf("codexoauth: parse token response: %w", err)
		}
		return &tr, nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		lower := strings.ToLower(string(body))
		if strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "refresh_token_reused") || strings.Contains(lower, "token_expired") {
			return nil, fmt.Errorf("%w: %s", ErrRefreshRotated, bodyStr)
		}
	}
	return nil, fmt.Errorf("%w: status=%d body=%s", ErrTokenEndpoint, resp.StatusCode, bodyStr)
}
