package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/codex"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
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
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, id, "", false)

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
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, cred.ID, "", true)

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
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{nil}, "", "", false)

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
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, "", "", false)

	if got := r.Credentials[0].Status; got != "expiring_soon" {
		t.Errorf("Status = %q, want expiring_soon", got)
	}
}

func TestBuildStatusReport_MissingTierIsNullInJSON(t *testing.T) {
	cred := fakeCred("dddd0000-0000-0000-0000-000000000001", "no-tier", "", 3600*1000)
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{{}}, "", "", false)

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
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, "", "", false)

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
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, cred.ID, "", false)

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
	r := buildStatusReport([]*store.Credential{cred}, []*oauth.UsageInfo{usage}, cred.ID, "", false)

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
	r := buildStatusReport(nil, nil, "", "", false)
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

// mkReportCmd builds a minimal cobra.Command with stdout wired to a
// *bytes.Buffer. Used to test renderStatusTable directly.
func mkReportCmd(buf *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.SetOut(buf)
	return cmd
}

func TestRenderStatusTable_ActiveMarker(t *testing.T) {
	tierVal := "Claude Pro"
	report := StatusReport{
		Version:  1,
		ActiveID: "aaaa0000-0000-0000-0000-000000000001",
		Credentials: []StatusEntry{
			{
				ID: "aaaa0000-0000-0000-0000-000000000001", Name: "work",
				Provider: "claude", Tier: &tierVal, Status: "valid",
				Active:    true,
				ExpiresAt: "2099-01-01T00:00:00Z", CreatedAt: "", LastRefreshedAt: "",
				Quota: QuotaStatus{Fetched: false},
			},
		},
	}
	var buf bytes.Buffer
	cmd := mkReportCmd(&buf)
	if err := renderStatusTable(cmd, report); err != nil {
		t.Fatalf("renderStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "*") {
		t.Errorf("active marker missing: %s", out)
	}
}

func TestRenderStatusTable_QuotaWindows(t *testing.T) {
	tierVal := "Claude Max 20x"
	report := StatusReport{
		Version: 1,
		Credentials: []StatusEntry{
			{
				ID: "bbbb0000-0000-0000-0000-000000000001", Name: "work",
				Provider: "claude", Tier: &tierVal, Status: "valid",
				ExpiresAt: "2099-01-01T00:00:00Z",
				Quota: QuotaStatus{
					Fetched: true,
					Windows: []QuotaWindow{
						{Name: "5h", Used: 25, ResetsAt: "2099-01-01T05:00:00Z"},
						{Name: "7d", Used: 0, ResetsAt: ""},
					},
				},
			},
		},
	}
	var buf bytes.Buffer
	cmd := mkReportCmd(&buf)
	if err := renderStatusTable(cmd, report); err != nil {
		t.Fatalf("renderStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "5h") {
		t.Errorf("quota window 5h missing: %s", out)
	}
	if !strings.Contains(out, "75%") {
		t.Errorf("quota remaining (75%%) missing: %s", out)
	}
}

func TestRenderStatusTable_QuotaError(t *testing.T) {
	tierVal := "Claude Pro"
	report := StatusReport{
		Version: 1,
		Credentials: []StatusEntry{
			{
				ID: "cccc0000-0000-0000-0000-000000000001", Name: "work",
				Provider: "claude", Tier: &tierVal, Status: "valid",
				ExpiresAt: "2099-01-01T00:00:00Z",
				Quota:     QuotaStatus{Fetched: true, Error: "HTTP 429"},
			},
		},
	}
	var buf bytes.Buffer
	cmd := mkReportCmd(&buf)
	if err := renderStatusTable(cmd, report); err != nil {
		t.Fatalf("renderStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "quota: error") {
		t.Errorf("quota error line missing: %s", out)
	}
}

func TestRenderStatusTable_CodexDetail(t *testing.T) {
	report := StatusReport{
		Version: 1,
		Credentials: []StatusEntry{
			{
				ID: "dddd0000-0000-0000-0000-000000000001", Name: "x1",
				Provider: "codex", Tier: nil, Detail: "pro x1@x.com",
				Status:    "valid",
				ExpiresAt: "2099-01-01T00:00:00Z",
				Quota:     QuotaStatus{Fetched: false},
			},
		},
	}
	var buf bytes.Buffer
	cmd := mkReportCmd(&buf)
	if err := renderStatusTable(cmd, report); err != nil {
		t.Fatalf("renderStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "pro x1@x.com") {
		t.Errorf("codex detail missing from table: %s", out)
	}
	if !strings.Contains(out, "codex") {
		t.Errorf("codex provider missing: %s", out)
	}
}

func TestRenderStatusTable_QuotaOverUtilization(t *testing.T) {
	// Used > 100 (over-utilization upstream) — remaining must clamp to 0.
	tierVal := "Claude Pro"
	report := StatusReport{
		Version: 1,
		Credentials: []StatusEntry{
			{
				ID: "ffff0000-0000-0000-0000-000000000001", Name: "over",
				Provider: "claude", Tier: &tierVal, Status: "valid",
				ExpiresAt: "2099-01-01T00:00:00Z",
				Quota: QuotaStatus{
					Fetched: true,
					Windows: []QuotaWindow{
						{Name: "5h", Used: 110, ResetsAt: ""},
					},
				},
			},
		},
	}
	var buf bytes.Buffer
	cmd := mkReportCmd(&buf)
	if err := renderStatusTable(cmd, report); err != nil {
		t.Fatalf("renderStatusTable: %v", err)
	}
	out := buf.String()
	// Remaining is clamped to 0, so should show "0%" not "-10%".
	if strings.Contains(out, "-") && strings.Contains(out, "10%") {
		t.Errorf("over-utilization not clamped to 0: %s", out)
	}
	if !strings.Contains(out, "0%") {
		t.Errorf("expected 0%% for over-utilization: %s", out)
	}
}

func TestRenderStatusTable_NameEqualsID(t *testing.T) {
	// When Name == ID, the table should abbreviate to first 8 chars + "...".
	id := "eeee0000-0000-0000-0000-000000000001"
	tierVal := "Claude Pro"
	report := StatusReport{
		Version: 1,
		Credentials: []StatusEntry{
			{
				ID: id, Name: id, Provider: "claude", Tier: &tierVal,
				Status:    "valid",
				ExpiresAt: "2099-01-01T00:00:00Z",
				Quota:     QuotaStatus{Fetched: false},
			},
		},
	}
	var buf bytes.Buffer
	cmd := mkReportCmd(&buf)
	if err := renderStatusTable(cmd, report); err != nil {
		t.Fatalf("renderStatusTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "...") {
		t.Errorf("abbreviated name missing: %s", out)
	}
}

func TestRelativeExpires_InvalidRFC3339_ReturnsInput(t *testing.T) {
	if got := relativeExpires("not-a-date"); got != "not-a-date" {
		t.Errorf("relativeExpires(invalid) = %q, want input unchanged", got)
	}
}

func TestRelativeExpires_JustNow(t *testing.T) {
	ts := time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339)
	if got := relativeExpires(ts); got != "just now" {
		t.Errorf("relativeExpires(5s ago) = %q, want just now", got)
	}
}

func TestRelativeExpires_MinsAgo(t *testing.T) {
	ts := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	got := relativeExpires(ts)
	if !strings.Contains(got, "mins ago") {
		t.Errorf("relativeExpires(10m ago) = %q, want Xmins ago", got)
	}
}

func TestRelativeExpires_HrsAgo(t *testing.T) {
	ts := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	got := relativeExpires(ts)
	if !strings.Contains(got, "hrs ago") {
		t.Errorf("relativeExpires(3h ago) = %q, want Xhrs ago", got)
	}
}

func TestRelativeExpires_InSecs(t *testing.T) {
	ts := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)
	got := relativeExpires(ts)
	if !strings.HasPrefix(got, "in ") || !strings.HasSuffix(got, "secs") {
		t.Errorf("relativeExpires(30s future) = %q, want in Xsecs", got)
	}
}

func TestRelativeExpires_InMins(t *testing.T) {
	ts := time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)
	got := relativeExpires(ts)
	if !strings.HasPrefix(got, "in ") || !strings.HasSuffix(got, "mins") {
		t.Errorf("relativeExpires(15m future) = %q, want in Xmins", got)
	}
}

func TestRelativeExpires_InHrs(t *testing.T) {
	ts := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	got := relativeExpires(ts)
	if !strings.HasPrefix(got, "in ") || !strings.HasSuffix(got, "hrs") {
		t.Errorf("relativeExpires(2h future) = %q, want in Xhrs", got)
	}
}

// setupFakeHome creates a temp dir, sets HOME/USERPROFILE, and creates
// ~/.ccm, ~/.claude, and ~/.codex dirs for cmd-level status tests that
// exercise both providers. It also pins the file backend for claude.
func setupFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, sub := range []string{".ccm", ".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(claude.UseFileBackendForTest())
	return home
}

// mkStatusJWT makes a minimal JWT with an email and expiry for status tests.
func mkStatusJWT(t *testing.T, email string) string {
	t.Helper()
	exp := time.Now().Add(time.Hour).Unix()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(
		`{"email":"` + email + `","exp":` + strconv.FormatInt(exp, 10) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"acct","chatgpt_plan_type":"pro"}}`,
	))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return h + "." + p + "." + s
}

// mkClaudeCredHelper builds a minimal claude credential for cmd status tests.
func mkClaudeCredHelper(t *testing.T, name string) *store.Credential {
	t.Helper()
	return &store.Credential{
		ID:   name + "-0000-0000-0000-000000000001",
		Name: name,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "at",
			RefreshToken: "rt",
			ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		},
		Subscription:    store.Subscription{Tier: "Claude Pro"},
		CreatedAt:       "2026-01-01T00:00:00Z",
		LastRefreshedAt: "2026-01-01T00:00:00Z",
	}
}

// mkCodexCredHelper builds a minimal codex credential for cmd status tests.
func mkCodexCredHelper(t *testing.T, name string) *store.Credential {
	t.Helper()
	tok := mkStatusJWT(t, name+"@x.com")
	return &store.Credential{
		ID:       name + "-1111-0000-0000-000000000002",
		Name:     name,
		Provider: "codex",
		ClaudeAiOauth: store.OAuthTokens{
			ExpiresAt: 0, // codex creds don't use ClaudeAiOauth
		},
		AuthMode: "chatgpt",
		Tokens: &store.CodexTokens{
			IDToken:      tok,
			AccessToken:  tok,
			RefreshToken: "rt",
			AccountID:    "acct",
		},
		CreatedAt:       "2026-01-01T00:00:00Z",
		LastRefreshedAt: "2026-01-01T00:00:00Z",
		LastRefresh:     "2026-01-01T00:00:00Z",
	}
}

// runStatusCmd runs `ccm status <args>` using rootCmd and returns stdout.
// It suppresses the claudeSyncFn side-effect and resets global state on cleanup.
func runStatusCmd(t *testing.T, args ...string) string {
	t.Helper()
	// Suppress the root PersistentPreRunE sync so tests don't touch
	// the real ~/.claude path.
	origSync := claudeSyncFn
	claudeSyncFn = func() (bool, error) { return false, nil }
	t.Cleanup(func() { claudeSyncFn = origSync })

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append([]string{"status"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute status %v: %v\nstderr: %s", args, err, stderr.String())
	}
	return stdout.String()
}

func TestStatus_AddsProviderColumn(t *testing.T) {
	setupFakeHome(t)
	if err := store.Save(mkClaudeCredHelper(t, "claude1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(mkCodexCredHelper(t, "codex1")); err != nil {
		t.Fatal(err)
	}
	out := runStatusCmd(t, "--no-quota")
	if !strings.Contains(out, "PROVIDER") {
		t.Fatalf("missing PROVIDER column: %s", out)
	}
	if !strings.Contains(out, "claude") || !strings.Contains(out, "codex") {
		t.Fatalf("provider values missing: %s", out)
	}
}

func TestStatus_JSON_AdditiveFields_VersionUnchanged(t *testing.T) {
	setupFakeHome(t)
	cc := mkClaudeCredHelper(t, "c1")
	if err := store.Save(cc); err != nil {
		t.Fatal(err)
	}
	if err := claude.Use(cc); err != nil {
		t.Fatal(err)
	}
	xc := mkCodexCredHelper(t, "x1")
	if err := store.Save(xc); err != nil {
		t.Fatal(err)
	}
	if err := codex.Use(xc); err != nil {
		t.Fatal(err)
	}
	out := runStatusCmd(t, "-o", "json", "--no-quota")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("json.Unmarshal: %v\noutput: %s", err, out)
	}

	if int(v["version"].(float64)) != 1 {
		t.Fatalf("Version must stay 1; got %v", v["version"])
	}
	activeID, _ := v["activeId"].(string)
	if activeID == "" {
		t.Fatalf("activeId must still hold the claude active id")
	}
	activeCodexID, _ := v["activeCodexId"].(string)
	if activeCodexID == "" {
		t.Fatalf("activeCodexId missing for active codex cred")
	}
	creds := v["credentials"].([]any)
	for _, c := range creds {
		m := c.(map[string]any)
		if _, ok := m["provider"]; !ok {
			t.Fatalf("entry missing provider field: %v", m)
		}
	}
}
