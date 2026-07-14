package grokoauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	grokmw "github.com/hbinhng/claude-credentials-manager/internal/grok/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// BillingURL is cli-chat-proxy's weekly-quota endpoint. The ?format=credits
// view reports the SuperGrok weekly window as config.creditUsagePercent (the
// number the grok /usage panel shows). Var so tests override.
// SOURCE: captured from grok-shell's own polling (mitmproxy, 2026-07-14).
var BillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"

// SettingsURL exposes subscription_tier_display (e.g. "SuperGrok Heavy").
var SettingsURL = "https://cli-chat-proxy.grok.com/v1/settings"

// FetchUsageFn is the seam ccm calls to fetch grok quota. Default is
// FetchUsage; tests inject canned responses without HTTP round-trips.
var FetchUsageFn = FetchUsage

// grokBillingResponse is the subset of /v1/billing?format=credits ccm reads.
type grokBillingResponse struct {
	Config struct {
		CreditUsagePercent float64 `json:"creditUsagePercent"`
		CurrentPeriod      struct {
			Type string `json:"type"`
			End  string `json:"end"`
		} `json:"currentPeriod"`
		BillingPeriodEnd string `json:"billingPeriodEnd"`
	} `json:"config"`
}

// FetchUsage returns grok's weekly credit usage as an Anthropic-shaped
// UsageInfo so `ccm status` renders it exactly like claude/codex windows.
// Tier ("SuperGrok Heavy") is fetched best-effort from /v1/settings and left
// empty on failure. On a billing failure (network, non-2xx, parse) the whole
// call fails-open with Error populated — same contract as the other fetchers.
func FetchUsage(accessToken string) *oauth.UsageInfo {
	if accessToken == "" {
		return &oauth.UsageInfo{Error: "missing access token"}
	}
	quotas, err := fetchWeeklyQuota(accessToken)
	if err != nil {
		return &oauth.UsageInfo{Error: err.Error()}
	}
	return &oauth.UsageInfo{Quotas: quotas, Tier: fetchTier(accessToken)}
}

func fetchWeeklyQuota(accessToken string) ([]oauth.Quota, error) {
	body, err := grokGet(BillingURL, accessToken)
	if err != nil {
		return nil, err
	}
	var r grokBillingResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse billing: %w", err)
	}
	resets := r.Config.CurrentPeriod.End
	if resets == "" {
		resets = r.Config.BillingPeriodEnd
	}
	return []oauth.Quota{{
		Name:     periodName(r.Config.CurrentPeriod.Type),
		Used:     r.Config.CreditUsagePercent,
		ResetsAt: resets,
	}}, nil
}

// fetchTier is best-effort: any failure yields "" (the quota still renders).
func fetchTier(accessToken string) string {
	body, err := grokGet(SettingsURL, accessToken)
	if err != nil {
		return ""
	}
	var s struct {
		SubscriptionTierDisplay string `json:"subscription_tier_display"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return ""
	}
	return s.SubscriptionTierDisplay
}

// periodName maps grok's USAGE_PERIOD_TYPE_* enum to a short window label,
// defaulting to "weekly" (the SuperGrok subscription window).
func periodName(t string) string {
	if rest, ok := strings.CutPrefix(t, "USAGE_PERIOD_TYPE_"); ok {
		return strings.ToLower(rest)
	}
	return "weekly"
}

// grokGet does an authenticated GET presenting as grok-shell (constant
// identity headers, no session/per-request headers — a quota poll, not a
// message). Returns the body on 2xx, else an error.
func grokGet(url, accessToken string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err // unreachable: method + URL are always well-formed
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	grokmw.ApplyGrokConstantIdentity(req) // present as grok-shell on the wire
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

