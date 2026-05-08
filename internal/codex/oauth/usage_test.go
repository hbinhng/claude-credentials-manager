package codexoauth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

func setUsageURL(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	prev := codexoauth.UsageURL
	codexoauth.UsageURL = srv.URL
	t.Cleanup(func() { codexoauth.UsageURL = prev })
}

func TestFetchUsage_HappyPath_BothWindows(t *testing.T) {
	var gotAuth, gotAccountID string
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("chatgpt-account-id")
		_, _ = w.Write([]byte(`{
			"rate_limit": {
				"primary_window":   {"used_percent": 45.2, "reset_at": 1900000000},
				"secondary_window": {"used_percent": 67.8, "reset_at": 1910000000}
			}
		}`))
	})
	info := codexoauth.FetchUsage("at-123", "acct-1")
	if info == nil || info.Error != "" {
		t.Fatalf("unexpected error: %+v", info)
	}
	if gotAuth != "Bearer at-123" {
		t.Fatalf("auth header: %q", gotAuth)
	}
	if gotAccountID != "acct-1" {
		t.Fatalf("account header: %q", gotAccountID)
	}
	if len(info.Quotas) != 2 {
		t.Fatalf("want 2 quotas; got %d", len(info.Quotas))
	}
	if info.Quotas[0].Name != "5h" || info.Quotas[0].Used != 45.2 {
		t.Fatalf("5h: %+v", info.Quotas[0])
	}
	if info.Quotas[1].Name != "7d" || info.Quotas[1].Used != 67.8 {
		t.Fatalf("7d: %+v", info.Quotas[1])
	}
}

func TestFetchUsage_NoAccountID_OmitsHeader(t *testing.T) {
	var has bool
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, has = r.Header["Chatgpt-Account-Id"]
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":10,"reset_at":1900000000}}}`))
	})
	codexoauth.FetchUsage("at", "")
	if has {
		t.Fatal("chatgpt-account-id header should be absent")
	}
}

func TestFetchUsage_ResetAfterSeconds_RelativeToNow(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":50,"reset_after_seconds":3600}}}`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || len(info.Quotas) != 1 {
		t.Fatalf("info: %+v", info)
	}
	parsed, err := time.Parse(time.RFC3339, info.Quotas[0].ResetsAt)
	if err != nil {
		t.Fatalf("ResetsAt parse: %v", err)
	}
	delta := time.Until(parsed)
	if delta < 50*time.Minute || delta > 70*time.Minute {
		t.Fatalf("expected ~1h relative reset; got %v", delta)
	}
}

func TestFetchUsage_OnlyPrimaryWindow(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":12,"reset_at":1900000000}}}`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || info.Error != "" {
		t.Fatalf("info: %+v", info)
	}
	if len(info.Quotas) != 1 {
		t.Fatalf("want 1 quota; got %d", len(info.Quotas))
	}
	if info.Quotas[0].Name != "5h" {
		t.Fatalf("name: %s", info.Quotas[0].Name)
	}
}

func TestFetchUsage_EmptyAccessToken_Errors(t *testing.T) {
	info := codexoauth.FetchUsage("", "x")
	if info == nil || info.Error == "" {
		t.Fatal("want error for empty token")
	}
}

func TestFetchUsage_HTTPError(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || !strings.Contains(info.Error, "HTTP 503") {
		t.Fatalf("want HTTP 503 in error: %+v", info)
	}
}

func TestFetchUsage_NetworkError(t *testing.T) {
	prev := codexoauth.UsageURL
	codexoauth.UsageURL = "http://127.0.0.1:1"
	defer func() { codexoauth.UsageURL = prev }()
	info := codexoauth.FetchUsage("at", "")
	if info == nil || info.Error == "" {
		t.Fatal("want network error")
	}
}

func TestFetchUsage_BadJSON_Errors(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || !strings.Contains(info.Error, "no rate-limit") {
		t.Fatalf("want parse fallthrough: %+v", info)
	}
}

func TestFetchUsage_EmptyRateLimit_Errors(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rate_limit":{}}`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || !strings.Contains(info.Error, "no rate-limit") {
		t.Fatalf("want no-windows error: %+v", info)
	}
}

func TestFetchUsage_InvalidURL_Errors(t *testing.T) {
	// Force http.NewRequest to fail by setting a URL containing a control
	// character (\x00), which is rejected by net/url.Parse.
	prev := codexoauth.UsageURL
	codexoauth.UsageURL = "http://host\x00invalid"
	defer func() { codexoauth.UsageURL = prev }()
	info := codexoauth.FetchUsage("at", "")
	if info == nil || info.Error == "" {
		t.Fatal("want error for invalid URL")
	}
}

func TestFetchUsage_PlanType_PopulatesTier(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"plan_type": "pro",
			"rate_limit": {
				"primary_window":   {"used_percent": 10, "reset_at": 1900000000},
				"secondary_window": {"used_percent": 20, "reset_at": 1910000000}
			}
		}`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || info.Error != "" {
		t.Fatalf("unexpected error: %+v", info)
	}
	if info.Tier != "Pro" {
		t.Fatalf("Tier = %q, want Pro", info.Tier)
	}
}

func TestFetchUsage_EmptyPlanType_TierEmpty(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"rate_limit": {
				"primary_window": {"used_percent": 10, "reset_at": 1900000000}
			}
		}`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || info.Error != "" {
		t.Fatalf("unexpected error: %+v", info)
	}
	if info.Tier != "" {
		t.Fatalf("Tier = %q, want empty when plan_type absent", info.Tier)
	}
}

func TestFetchUsage_AdditionalRateLimits_EmittedAsExtraQuotas(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"plan_type": "pro",
			"rate_limit": {
				"primary_window":   {"used_percent": 10, "reset_at": 1900000000},
				"secondary_window": {"used_percent": 20, "reset_at": 1910000000}
			},
			"additional_rate_limits": [
				{
					"limit_name": "GPT-5.3-Codex-Spark",
					"metered_feature": "codex_bengalfox",
					"rate_limit": {
						"primary_window":   {"used_percent": 30, "reset_at": 1900000000},
						"secondary_window": {"used_percent": 40, "reset_at": 1910000000}
					}
				}
			]
		}`))
	})
	info := codexoauth.FetchUsage("at", "acct")
	if info == nil || info.Error != "" {
		t.Fatalf("unexpected error: %+v", info)
	}
	// Expect 4 quotas: 5h, 7d, 5h/gpt-5.3-codex-spark, 7d/gpt-5.3-codex-spark
	if len(info.Quotas) != 4 {
		t.Fatalf("want 4 quotas; got %d: %+v", len(info.Quotas), info.Quotas)
	}
	// Build a name→quota map for easier assertion.
	byName := make(map[string]float64)
	for _, q := range info.Quotas {
		byName[q.Name] = q.Used
	}
	if byName["5h"] != 10 {
		t.Fatalf("5h.Used = %v, want 10", byName["5h"])
	}
	if byName["7d"] != 20 {
		t.Fatalf("7d.Used = %v, want 20", byName["7d"])
	}
	if byName["5h/gpt-5.3-codex-spark"] != 30 {
		t.Fatalf("5h/gpt-5.3-codex-spark.Used = %v, want 30", byName["5h/gpt-5.3-codex-spark"])
	}
	if byName["7d/gpt-5.3-codex-spark"] != 40 {
		t.Fatalf("7d/gpt-5.3-codex-spark.Used = %v, want 40", byName["7d/gpt-5.3-codex-spark"])
	}
	if info.Tier != "Pro" {
		t.Fatalf("Tier = %q, want Pro", info.Tier)
	}
}

func TestFetchUsage_AdditionalRateLimits_EmptyLimitName_Skipped(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"rate_limit": {
				"primary_window": {"used_percent": 10, "reset_at": 1900000000}
			},
			"additional_rate_limits": [
				{
					"limit_name": "",
					"rate_limit": {
						"primary_window": {"used_percent": 50, "reset_at": 1900000000}
					}
				}
			]
		}`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || info.Error != "" {
		t.Fatalf("unexpected error: %+v", info)
	}
	// Only the top-level 5h window; empty limit_name entry is skipped.
	if len(info.Quotas) != 1 {
		t.Fatalf("want 1 quota; got %d", len(info.Quotas))
	}
	if info.Quotas[0].Name != "5h" {
		t.Fatalf("quota name = %q, want 5h", info.Quotas[0].Name)
	}
}

func TestFetchUsage_PlanType_FreePlan_Capitalized(t *testing.T) {
	setUsageURL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"plan_type": "free",
			"rate_limit": {
				"primary_window": {"used_percent": 5, "reset_at": 1900000000}
			}
		}`))
	})
	info := codexoauth.FetchUsage("at", "")
	if info == nil || info.Error != "" {
		t.Fatalf("unexpected error: %+v", info)
	}
	if info.Tier != "Free" {
		t.Fatalf("Tier = %q, want Free", info.Tier)
	}
}
