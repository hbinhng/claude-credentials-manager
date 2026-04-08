package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// ProfileURL can be overridden in tests.
var ProfileURL = "https://api.anthropic.com/api/oauth/profile"

type Profile struct {
	Email    string
	FullName string
}

// FetchProfile calls the OAuth profile endpoint and returns the account email and name.
// Returns an empty Profile if the request fails.
func FetchProfile(accessToken string) Profile {
	req, err := http.NewRequest("GET", ProfileURL, nil)
	if err != nil {
		return Profile{}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Profile{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Profile{}
	}

	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Account struct {
			Email    string `json:"email"`
			FullName string `json:"full_name"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Profile{}
	}
	return Profile{
		Email:    payload.Account.Email,
		FullName: payload.Account.FullName,
	}
}
