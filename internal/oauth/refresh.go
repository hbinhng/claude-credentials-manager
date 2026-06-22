package oauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
)

// ErrInvalidGrant marks a refresh failure where the OAuth token endpoint
// rejected the refresh token itself (error code "invalid_grant"): the token is
// revoked or expired and will not recover until the user re-authenticates with
// `ccm login`. Callers errors.Is on it to distinguish a permanently-dead
// credential from a transient refresh failure (network / 5xx / other 4xx).
var ErrInvalidGrant = errors.New("oauth: refresh token invalid_grant")

// oauthErrorCode extracts the OAuth2 "error" field from a token-endpoint error
// body. Returns "" when the body is not JSON or carries no error field.
func oauthErrorCode(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error
}

// Refresh exchanges a refresh token for a new access token.
func Refresh(refreshToken string) (*TokenResponse, error) {
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClientID,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", TokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: httpx.Transport(), Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if oauthErrorCode(respBody) == "invalid_grant" {
			return nil, fmt.Errorf("refresh failed (HTTP %d): %s: %w", resp.StatusCode, string(respBody), ErrInvalidGrant)
		}
		return nil, fmt.Errorf("refresh failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(respBody, &tokens); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	return &tokens, nil
}
