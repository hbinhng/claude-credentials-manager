package cmd

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// fakeCred builds a minimal store.Credential with an ExpiresAt relative
// to now. Positive offsetMs = valid, negative = expired.
func fakeCred(id, name, tier string, offsetMs int64) *store.Credential {
	return &store.Credential{
		ID:   id,
		Name: name,
		ClaudeAiOauth: store.OAuthTokens{
			ExpiresAt: time.Now().UnixMilli() + offsetMs,
		},
		Subscription:    store.Subscription{Tier: tier},
		CreatedAt:       "2025-01-15T10:30:00Z",
		LastRefreshedAt: "2026-04-10T12:00:00Z",
	}
}

func TestBuildStatusReport_ValidCredentialWithQuota(t *testing.T) {
	const id = "4300c4bc-c04d-4b1f-8609-6c7b518de3df"
	cred := fakeCred(id, "work", "Claude Max 20x", 3600*1000)
	usage := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 25, ResetsAt: "2026-04-11T15:30:00Z"},
			{Name: "7d", Used: 40, ResetsAt: "2026-04-17T10:30:00Z"},
		},
	}
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, id, false)

	if r.Version != 1 {
		t.Errorf("Version = %d, want 1", r.Version)
	}
	if r.ActiveID != id {
		t.Errorf("ActiveID = %q, want %q", r.ActiveID, id)
	}
	if len(r.Credentials) != 1 {
		t.Fatalf("Credentials len = %d, want 1", len(r.Credentials))
	}
	e := r.Credentials[0]
	if e.ID != id {
		t.Errorf("ID = %q, want %q", e.ID, id)
	}
	if e.Name != "work" {
		t.Errorf("Name = %q, want work", e.Name)
	}
	if e.Tier == nil || *e.Tier != "Claude Max 20x" {
		t.Errorf("Tier = %v, want pointer to Claude Max 20x", e.Tier)
	}
	if e.Status != "valid" {
		t.Errorf("Status = %q, want valid", e.Status)
	}
	if !e.Active {
		t.Errorf("Active = false, want true")
	}
	if e.CreatedAt != "2025-01-15T10:30:00Z" {
		t.Errorf("CreatedAt = %q", e.CreatedAt)
	}
	if e.LastRefreshedAt != "2026-04-10T12:00:00Z" {
		t.Errorf("LastRefreshedAt = %q", e.LastRefreshedAt)
	}
	if !strings.HasSuffix(e.ExpiresAt, "Z") {
		t.Errorf("ExpiresAt = %q, want RFC3339 UTC (Z suffix)", e.ExpiresAt)
	}
	if !e.Quota.Fetched {
		t.Errorf("Quota.Fetched = false, want true")
	}
	if e.Quota.Error != "" {
		t.Errorf("Quota.Error = %q, want empty", e.Quota.Error)
	}
	if len(e.Quota.Windows) != 2 {
		t.Fatalf("Quota.Windows len = %d, want 2", len(e.Quota.Windows))
	}
	if e.Quota.Windows[0].Name != "5h" {
		t.Errorf("Windows[0].Name = %q", e.Quota.Windows[0].Name)
	}
	if e.Quota.Windows[0].Used != 25 {
		t.Errorf("Windows[0].Used = %v", e.Quota.Windows[0].Used)
	}
	if e.Quota.Windows[0].ResetsAt != "2026-04-11T15:30:00Z" {
		t.Errorf("Windows[0].ResetsAt = %q, want raw RFC3339", e.Quota.Windows[0].ResetsAt)
	}
}

func TestBuildStatusReport_NoQuotaFlagSuppressesFetch(t *testing.T) {
	cred := fakeCred("aaaa0000-0000-0000-0000-000000000001", "one", "Claude Pro", 3600*1000)
	// Even if usages are passed (e.g. caller populated them by mistake),
	// the --no-quota flag must suppress them in the report so consumers
	// always see a consistent "not fetched" signal.
	usage := &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10, ResetsAt: "2026-04-11T15:30:00Z"}}}
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, cred.ID, true)

	e := r.Credentials[0]
	if e.Quota.Fetched {
		t.Errorf("Quota.Fetched = true, want false under --no-quota")
	}
	if len(e.Quota.Windows) != 0 {
		t.Errorf("Quota.Windows = %+v, want none under --no-quota", e.Quota.Windows)
	}
	if e.Quota.Error != "" {
		t.Errorf("Quota.Error = %q, want empty under --no-quota", e.Quota.Error)
	}
}

func TestBuildStatusReport_ExpiredCredentialSkipsQuota(t *testing.T) {
	cred := fakeCred("bbbb0000-0000-0000-0000-000000000001", "old", "Claude Pro", -3600*1000)
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{nil}, "", false)

	e := r.Credentials[0]
	if e.Status != "expired" {
		t.Errorf("Status = %q, want expired", e.Status)
	}
	if e.Active {
		t.Errorf("Active = true, want false (activeID empty)")
	}
	if e.Quota.Fetched {
		t.Errorf("Quota.Fetched = true, want false for expired")
	}
}

func TestBuildStatusReport_StatusNormalized(t *testing.T) {
	// Credential expiring in 60 seconds — IsExpiringSoon threshold is
	// 5 minutes, so Status() returns "expiring soon" (space-separated).
	// The JSON form must be snake_case.
	cred := fakeCred("cccc0000-0000-0000-0000-000000000001", "soon", "Claude Pro", 60*1000)
	usage := &oauth.UsageInfo{}
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, "", false)

	if got := r.Credentials[0].Status; got != "expiring_soon" {
		t.Errorf("Status = %q, want expiring_soon", got)
	}
}

func TestBuildStatusReport_MissingTierIsNullInJSON(t *testing.T) {
	cred := fakeCred("dddd0000-0000-0000-0000-000000000001", "no-tier", "", 3600*1000)
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{{}}, "", false)

	e := r.Credentials[0]
	if e.Tier != nil {
		t.Errorf("Tier = %v, want nil for empty tier", *e.Tier)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"tier":null`) {
		t.Errorf("marshalled entry = %s, want to contain \"tier\":null", string(raw))
	}
}

func TestBuildStatusReport_QuotaFetchErrorSurfaced(t *testing.T) {
	cred := fakeCred("eeee0000-0000-0000-0000-000000000001", "rate-limited", "Claude Max 20x", 3600*1000)
	usage := &oauth.UsageInfo{Error: "HTTP 429: too many requests"}
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, "", false)

	e := r.Credentials[0]
	if !e.Quota.Fetched {
		t.Errorf("Quota.Fetched = false, want true (we DID attempt the fetch)")
	}
	if e.Quota.Error != "HTTP 429: too many requests" {
		t.Errorf("Quota.Error = %q", e.Quota.Error)
	}
	if len(e.Quota.Windows) != 0 {
		t.Errorf("Quota.Windows should be empty when error is set, got %+v", e.Quota.Windows)
	}
}

func TestWriteStatusJSON_NoRemainingField(t *testing.T) {
	// The quota windows in JSON output carry only `used` — consumers
	// that want "remaining" can compute `100 - used` themselves. Having
	// both fields in the envelope is redundant and wastes bytes.
	cred := fakeCred("4300c4bc-c04d-4b1f-8609-6c7b518de3df", "work", "Claude Max 20x", 3600*1000)
	usage := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 25, ResetsAt: "2026-04-11T15:30:00Z"},
		},
	}
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, cred.ID, false)

	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, r); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, `"remaining"`) {
		t.Errorf("JSON output still contains \"remaining\" field:\n%s", out)
	}
	if !strings.Contains(out, `"used":25`) {
		t.Errorf("JSON output missing \"used\":25 :\n%s", out)
	}
}

func TestWriteStatusJSON_Minified(t *testing.T) {
	// `-o json` must emit minified JSON (one line + trailing newline) so
	// pipelines stay compact. Consumers who want pretty output can pipe
	// through `jq`.
	cred := fakeCred("4300c4bc-c04d-4b1f-8609-6c7b518de3df", "work", "Claude Max 20x", 3600*1000)
	usage := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 25, ResetsAt: "2026-04-11T15:30:00Z"},
		},
	}
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, cred.ID, false)

	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, r); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output does not end with newline: %q", out)
	}
	body := strings.TrimSuffix(out, "\n")
	if strings.Contains(body, "\n") {
		t.Errorf("output is not minified (contains embedded newline):\n%s", out)
	}
	if strings.Contains(body, "  ") {
		t.Errorf("output is not minified (contains double-space indent): %q", body)
	}
	// Sanity: it must still round-trip as a valid envelope.
	var decoded StatusReport
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("minified output did not round-trip: %v\n%s", err, body)
	}
	if len(decoded.Credentials) != 1 || decoded.Credentials[0].Name != "work" {
		t.Errorf("decoded envelope wrong: %+v", decoded)
	}
}

func TestBuildStatusReport_EnvelopeJSONShape(t *testing.T) {
	// End-to-end: the envelope marshals with version + activeId +
	// credentials keys, with credentials as an array (never null), so
	// scripts can always run `jq '.credentials[]'`.
	r := buildStatusReport(nil, nil, "", false)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"version":1`) {
		t.Errorf("envelope = %s, want version:1", s)
	}
	if !strings.Contains(s, `"credentials":[]`) {
		t.Errorf("envelope = %s, want credentials:[] (not null)", s)
	}
}

func TestFetchUsagesParallel_UsesFetchUsageFnSeam(t *testing.T) {
	orig := oauth.FetchUsageFn
	defer func() { oauth.FetchUsageFn = orig }()

	tokens := make([]string, 0, 2)
	var mu sync.Mutex
	oauth.FetchUsageFn = func(token string) *oauth.UsageInfo {
		mu.Lock()
		tokens = append(tokens, token)
		mu.Unlock()
		return &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 10, ResetsAt: "2099-01-01T00:00:00Z"}}}
	}

	valid := fakeCred("1111aaaa-0000-0000-0000-000000000001", "valid", "Claude Pro", 3600*1000)
	valid.ClaudeAiOauth.AccessToken = "tok-valid"
	expired := fakeCred("2222bbbb-0000-0000-0000-000000000002", "old", "Claude Pro", -3600*1000)
	expired.ClaudeAiOauth.AccessToken = "tok-expired"
	another := fakeCred("3333cccc-0000-0000-0000-000000000003", "other", "Claude Pro", 3600*1000)
	another.ClaudeAiOauth.AccessToken = "tok-other"

	usages := fetchUsagesParallel([]*store.Credential{valid, expired, another})

	if len(usages) != 3 {
		t.Fatalf("len(usages) = %d, want 3", len(usages))
	}
	if usages[0] == nil || len(usages[0].Quotas) != 1 {
		t.Errorf("usages[0] not populated from seam: %+v", usages[0])
	}
	if usages[1] != nil {
		t.Errorf("usages[1] should be nil for expired credential, got %+v", usages[1])
	}
	if usages[2] == nil || len(usages[2].Quotas) != 1 {
		t.Errorf("usages[2] not populated from seam: %+v", usages[2])
	}

	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 2 {
		t.Errorf("seam called %d times, want 2 (expired skipped)", len(tokens))
	}
	sort.Strings(tokens)
	if tokens[0] != "tok-other" || tokens[1] != "tok-valid" {
		t.Errorf("seam tokens = %v, want [tok-other tok-valid]", tokens)
	}
}
