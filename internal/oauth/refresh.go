package oauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Refresh exchanges a refresh token for a new access token.
func Refresh(refreshToken string) (*TokenResponse, error) {
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClientID,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(TokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(respBody, &tokens); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	return &tokens, nil
}
