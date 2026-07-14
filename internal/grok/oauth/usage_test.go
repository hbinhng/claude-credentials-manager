package grokoauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// billingBody is the real cli-chat-proxy /v1/billing?format=credits shape
// (captured 2026-07-14): a weekly SuperGrok window reported as a percent.
const billingBody = `{"config":{
  "currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-07-13T04:01:19.173373+00:00","end":"2026-07-20T04:01:19.173373+00:00"},
  "creditUsagePercent":56.0,
  "onDemandCap":{"val":0},"onDemandUsed":{"val":0},
  "productUsage":[{"product":"Api","usagePercent":56.0},{"product":"GrokBuild"}],
  "billingPeriodEnd":"2026-07-20T04:01:19.173373+00:00"}}`

const settingsBody = `{"subscription_tier_display":"SuperGrok Heavy","min_client_version":"0.1.202"}`

func TestFetchUsage_WeeklyQuotaAndTier(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.grok → default UA version
	var gotBillingAuth, gotBillingUA, gotIdent string
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBillingAuth = r.Header.Get("Authorization")
		gotBillingUA = r.Header.Get("User-Agent")
		gotIdent = r.Header.Get("x-grok-client-identifier")
		_, _ = w.Write([]byte(billingBody))
	}))
	defer billing.Close()
	settings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(settingsBody))
	}))
	defer settings.Close()

	oldB, oldS := BillingURL, SettingsURL
	BillingURL, SettingsURL = billing.URL, settings.URL
	defer func() { BillingURL, SettingsURL = oldB, oldS }()

	info := FetchUsage("tok-abc")
	if info.Error != "" {
		t.Fatalf("unexpected error: %s", info.Error)
	}
	if len(info.Quotas) != 1 {
		t.Fatalf("want 1 quota, got %d", len(info.Quotas))
	}
	q := info.Quotas[0]
	if q.Name != "weekly" {
		t.Errorf("quota name = %q, want weekly", q.Name)
	}
	if q.Used != 56.0 {
		t.Errorf("quota used = %v, want 56.0", q.Used)
	}
	if q.ResetsAt != "2026-07-20T04:01:19.173373+00:00" {
		t.Errorf("resetsAt = %q, want the currentPeriod.end", q.ResetsAt)
	}
	if info.Tier != "SuperGrok Heavy" {
		t.Errorf("tier = %q, want SuperGrok Heavy", info.Tier)
	}
	// Authentic grok-shell identity on the billing GET.
	if gotBillingAuth != "Bearer tok-abc" {
		t.Errorf("Authorization = %q", gotBillingAuth)
	}
	if !strings.HasPrefix(gotBillingUA, "grok-shell/") {
		t.Errorf("User-Agent = %q, want grok-shell/ prefix", gotBillingUA)
	}
	if gotIdent != "grok-shell" {
		t.Errorf("x-grok-client-identifier = %q, want grok-shell", gotIdent)
	}
}

func TestFetchUsage_MissingToken(t *testing.T) {
	if got := FetchUsage(""); got.Error == "" {
		t.Error("empty token should yield an Error")
	}
}

func TestFetchUsage_BillingNon200(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer billing.Close()
	oldB := BillingURL
	BillingURL = billing.URL
	defer func() { BillingURL = oldB }()

	info := FetchUsage("tok")
	if info.Error == "" {
		t.Error("billing 403 should surface as Error")
	}
	if len(info.Quotas) != 0 {
		t.Error("no quotas on billing failure")
	}
}

func TestFetchUsage_BillingBadJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer billing.Close()
	oldB := BillingURL
	BillingURL = billing.URL
	defer func() { BillingURL = oldB }()

	if info := FetchUsage("tok"); info.Error == "" {
		t.Error("unparseable billing body should surface as Error")
	}
}

func TestFetchUsage_TierBestEffort(t *testing.T) {
	// Billing OK but settings fails → quota still returned, Tier empty.
	t.Setenv("HOME", t.TempDir())
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(billingBody))
	}))
	defer billing.Close()
	settings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer settings.Close()
	oldB, oldS := BillingURL, SettingsURL
	BillingURL, SettingsURL = billing.URL, settings.URL
	defer func() { BillingURL, SettingsURL = oldB, oldS }()

	info := FetchUsage("tok")
	if info.Error != "" {
		t.Fatalf("tier failure must not fail the whole fetch: %s", info.Error)
	}
	if len(info.Quotas) != 1 {
		t.Fatalf("quota should still be present, got %d", len(info.Quotas))
	}
	if info.Tier != "" {
		t.Errorf("tier should be empty when settings fails, got %q", info.Tier)
	}
}

func TestFetchUsage_TransportError(t *testing.T) {
	// Point billing at a closed server → transport error.
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	oldB := BillingURL
	BillingURL = url
	defer func() { BillingURL = oldB }()

	if info := FetchUsage("tok"); info.Error == "" {
		t.Error("transport error should surface as Error")
	}
}

func TestFetchUsage_ResetsFallbackToBillingPeriodEnd(t *testing.T) {
	// currentPeriod.end absent → fall back to config.billingPeriodEnd.
	t.Setenv("HOME", t.TempDir())
	const body = `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY"},"creditUsagePercent":10,"billingPeriodEnd":"2026-08-01T00:00:00+00:00"}}`
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer billing.Close()
	settings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(settingsBody))
	}))
	defer settings.Close()
	oldB, oldS := BillingURL, SettingsURL
	BillingURL, SettingsURL = billing.URL, settings.URL
	defer func() { BillingURL, SettingsURL = oldB, oldS }()

	info := FetchUsage("tok")
	if len(info.Quotas) != 1 || info.Quotas[0].ResetsAt != "2026-08-01T00:00:00+00:00" {
		t.Fatalf("resetsAt should fall back to billingPeriodEnd, got %+v", info.Quotas)
	}
}

func TestFetchTier_BadSettingsJSON(t *testing.T) {
	// Settings returns 200 but unparseable → Tier empty, quota still returned.
	t.Setenv("HOME", t.TempDir())
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(billingBody))
	}))
	defer billing.Close()
	settings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer settings.Close()
	oldB, oldS := BillingURL, SettingsURL
	BillingURL, SettingsURL = billing.URL, settings.URL
	defer func() { BillingURL, SettingsURL = oldB, oldS }()

	info := FetchUsage("tok")
	if info.Error != "" || info.Tier != "" {
		t.Errorf("bad settings JSON → Tier empty, no error; got tier=%q err=%q", info.Tier, info.Error)
	}
}

func TestPeriodName(t *testing.T) {
	cases := map[string]string{
		"USAGE_PERIOD_TYPE_WEEKLY":  "weekly",
		"USAGE_PERIOD_TYPE_MONTHLY": "monthly",
		"":                          "weekly",
		"something-else":            "weekly",
	}
	for in, want := range cases {
		if got := periodName(in); got != want {
			t.Errorf("periodName(%q) = %q, want %q", in, got, want)
		}
	}
}
