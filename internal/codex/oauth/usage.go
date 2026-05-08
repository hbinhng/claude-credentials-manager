package codexoauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// UsageURL is OpenAI/ChatGPT's codex quota endpoint. Tests override.
var UsageURL = "https://chatgpt.com/backend-api/wham/usage"

// FetchUsageFn is the function ccm calls to fetch codex quota usage.
// Default points at FetchUsage; tests may override to inject canned
// responses without spinning up an httptest server.
var FetchUsageFn = FetchUsage

// FetchUsage queries the wham/usage endpoint for the given access
// token and optional ChatGPT account id (sent as chatgpt-account-id
// header). Returns a UsageInfo with Quotas populated for the 5h and 7d
// top-level windows plus any per-model windows from additional_rate_limits.
// Tier is populated from plan_type ("pro" → "Pro"). On any failure
// (network, non-2xx, parse error) returns a UsageInfo with Error
// populated — fail-open like the claude fetcher.
func FetchUsage(accessToken, accountID string) *oauth.UsageInfo {
	if accessToken == "" {
		return &oauth.UsageInfo{Error: "missing access token"}
	}
	req, err := http.NewRequest("GET", UsageURL, nil)
	if err != nil {
		return &oauth.UsageInfo{Error: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &oauth.UsageInfo{Error: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode != http.StatusOK {
		return &oauth.UsageInfo{Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	info := parseUsageResponse(body)
	if info == nil {
		return &oauth.UsageInfo{Error: "no rate-limit windows in response"}
	}
	return info
}

type codexWindow struct {
	UsedPercent       float64 `json:"used_percent"`
	ResetAt           int64   `json:"reset_at"`
	ResetAfterSeconds int64   `json:"reset_after_seconds"`
}

type codexAdditionalRateLimit struct {
	LimitName string `json:"limit_name"`
	RateLimit struct {
		Primary   codexWindow `json:"primary_window"`
		Secondary codexWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type codexUsageResponse struct {
	PlanType  string `json:"plan_type"`
	RateLimit struct {
		Primary   codexWindow `json:"primary_window"`
		Secondary codexWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	AdditionalRateLimits []codexAdditionalRateLimit `json:"additional_rate_limits"`
}

func parseUsageResponse(body []byte) *oauth.UsageInfo {
	var raw codexUsageResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	var quotas []oauth.Quota
	if q := windowToQuota("5h", raw.RateLimit.Primary); q != nil {
		quotas = append(quotas, *q)
	}
	if q := windowToQuota("7d", raw.RateLimit.Secondary); q != nil {
		quotas = append(quotas, *q)
	}

	// Parse per-model windows from additional_rate_limits.
	for _, entry := range raw.AdditionalRateLimits {
		if entry.LimitName == "" {
			continue
		}
		shortName := strings.ToLower(entry.LimitName)
		if q := windowToQuota("5h/"+shortName, entry.RateLimit.Primary); q != nil {
			quotas = append(quotas, *q)
		}
		if q := windowToQuota("7d/"+shortName, entry.RateLimit.Secondary); q != nil {
			quotas = append(quotas, *q)
		}
	}

	if len(quotas) == 0 {
		return nil
	}

	// Capitalize plan_type for display ("pro" → "Pro").
	var tier string
	if raw.PlanType != "" {
		tier = strings.ToUpper(raw.PlanType[:1]) + raw.PlanType[1:]
	}

	return &oauth.UsageInfo{Quotas: quotas, Tier: tier}
}

func windowToQuota(name string, w codexWindow) *oauth.Quota {
	if w.UsedPercent == 0 && w.ResetAt == 0 && w.ResetAfterSeconds == 0 {
		return nil
	}
	var resetsAt string
	if w.ResetAt > 0 {
		resetsAt = time.Unix(w.ResetAt, 0).UTC().Format(time.RFC3339)
	} else if w.ResetAfterSeconds > 0 {
		resetsAt = time.Now().Add(time.Duration(w.ResetAfterSeconds) * time.Second).UTC().Format(time.RFC3339)
	}
	return &oauth.Quota{Name: name, Used: w.UsedPercent, ResetsAt: resetsAt}
}
