package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
)

// ProfileURL can be overridden in tests.
var ProfileURL = "https://api.anthropic.com/api/oauth/profile"

type Profile struct {
	Email    string
	FullName string
	Tier     string // human-readable tier, e.g. "Claude Max 20x"
}

// FetchProfile calls the OAuth profile endpoint and returns the account email, name, and tier.
// Returns an empty Profile if the request fails.
func FetchProfile(accessToken string) Profile {
	req, err := http.NewRequest("GET", ProfileURL, nil)
	if err != nil {
		return Profile{}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	client := &http.Client{Transport: httpx.Transport(), Timeout: 10 * time.Second}
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
		Organization struct {
			RateLimitTier string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Profile{}
	}
	return Profile{
		Email:    payload.Account.Email,
		FullName: payload.Account.FullName,
		Tier:     formatTier(payload.Organization.RateLimitTier),
	}
}

// formatTier converts a rate_limit_tier like "default_claude_max_20x" into "Claude Max 20x".
func formatTier(raw string) string {
	if raw == "" {
		return ""
	}
	s := strings.TrimPrefix(raw, "default_")
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		// Keep things like "20x" lowercase-x
		if len(p) > 1 && p[len(p)-1] == 'x' && isDigit(p[0]) {
			parts[i] = p
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
