package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
)

// UsageURL can be overridden in tests.
var UsageURL = "https://api.anthropic.com/api/oauth/usage"

type UsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type UsageResponse struct {
	FiveHour   *UsageWindow           `json:"five_hour"`
	SevenDay   *UsageWindow           `json:"seven_day"`
	ExtraUsage any                    `json:"extra_usage"`
	Extra      map[string]UsageWindow `json:"-"`
}

func (r *UsageResponse) UnmarshalJSON(data []byte) error {
	// First unmarshal known fields
	type Alias UsageResponse
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*r = UsageResponse(alias)

	// Then unmarshal all fields to catch seven_day_* model windows
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil // non-fatal
	}
	r.Extra = make(map[string]UsageWindow)
	for key, val := range raw {
		if len(key) > 10 && key[:10] == "seven_day_" {
			var w UsageWindow
			if json.Unmarshal(val, &w) == nil && w.Utilization != nil {
				r.Extra[key] = w
			}
		}
	}
	return nil
}

type Quota struct {
	Name     string
	Used     float64 // percentage used (0-100; may exceed 100 if upstream reports over-utilization)
	ResetsAt string  // raw RFC3339 upstream timestamp; call FormatResetTime to render
}

type UsageInfo struct {
	Quotas []Quota
	Error  string // non-empty if fetch failed
}

// FetchUsageFn is the function the rest of ccm calls to fetch quota
// usage. By default it points at FetchUsage; tests may override it to
// inject a canned response without spinning up an httptest server.
// This is a deliberately small consumer-side seam — the HTTP-level
// UsageURL override stays available for end-to-end tests in this
// package.
var FetchUsageFn = FetchUsage

// FetchUsage calls the Claude OAuth usage endpoint and returns quota info.
func FetchUsage(accessToken string) *UsageInfo {
	req, err := http.NewRequest("GET", UsageURL, nil)
	if err != nil {
		return &UsageInfo{Error: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Transport: httpx.Transport(), Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &UsageInfo{Error: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return &UsageInfo{Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 100))}
	}

	var usage UsageResponse
	if err := json.Unmarshal(body, &usage); err != nil {
		return &UsageInfo{Error: "invalid response"}
	}

	var quotas []Quota

	if usage.FiveHour != nil && usage.FiveHour.Utilization != nil {
		quotas = append(quotas, Quota{
			Name:     "5h",
			Used:     *usage.FiveHour.Utilization,
			ResetsAt: usage.FiveHour.ResetsAt,
		})
	}

	if usage.SevenDay != nil && usage.SevenDay.Utilization != nil {
		quotas = append(quotas, Quota{
			Name:     "7d",
			Used:     *usage.SevenDay.Utilization,
			ResetsAt: usage.SevenDay.ResetsAt,
		})
	}

	for key, w := range usage.Extra {
		model := key[10:] // strip "seven_day_"
		quotas = append(quotas, Quota{
			Name:     "7d/" + model,
			Used:     *w.Utilization,
			ResetsAt: w.ResetsAt,
		})
	}

	return &UsageInfo{Quotas: quotas}
}

// FormatResetTime converts an RFC3339 quota reset timestamp into a short
// human-readable relative string (e.g. "in 1h12m", "in 3d4h", "now").
// Returns "" for an empty input, and the original string on parse
// failure — upstream may change shape and we'd rather surface something
// than swallow it.
func FormatResetTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return s
		}
	}
	diff := time.Until(t)
	if diff <= 0 {
		return "now"
	}
	if diff < time.Hour {
		return fmt.Sprintf("in %dm", int(diff.Minutes()))
	}
	if diff >= 48*time.Hour {
		days := int(diff.Hours()) / 24
		hours := int(diff.Hours()) % 24
		return fmt.Sprintf("in %dd%dh", days, hours)
	}
	return fmt.Sprintf("in %dh%dm", int(diff.Hours()), int(diff.Minutes())%60)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
