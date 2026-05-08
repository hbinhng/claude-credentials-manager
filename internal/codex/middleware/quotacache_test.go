package middleware_test

import (
	"net/http"
	"testing"
	"time"

	codexmw "github.com/hbinhng/claude-credentials-manager/internal/codex/middleware"
)

// fakeUsageCache records calls to Update.
type fakeUsageCache struct {
	calls []usageCacheCall
}

type usageCacheCall struct {
	name        string
	usedPercent float64
	resetAt     time.Time
}

func (f *fakeUsageCache) Update(name string, usedPercent float64, resetAt time.Time) {
	f.calls = append(f.calls, usageCacheCall{name, usedPercent, resetAt})
}

func makeResp(headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{Header: h}
}

// TestQuotaCache_ParsesHeaders verifies that both 5h and 7d headers are
// parsed and forwarded to the UsageCache.
func TestQuotaCache_ParsesHeaders(t *testing.T) {
	fake := &fakeUsageCache{}
	qc := codexmw.NewQuotaCache(fake)

	resetTime := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	resp := makeResp(map[string]string{
		"x-codex-5h-used":       "0.42",
		"x-codex-5h-resets-at":  resetTime.Format(time.RFC3339),
		"x-codex-7d-used":       "0.77",
		"x-codex-7d-resets-at":  resetTime.Add(time.Hour * 24).Format(time.RFC3339),
	})

	qc.Apply(resp)

	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(fake.calls))
	}
	// Calls are emitted in order: 5h then 7d.
	if fake.calls[0].name != "5h" || fake.calls[0].usedPercent != 0.42 {
		t.Errorf("5h call = %+v, want name=5h used=0.42", fake.calls[0])
	}
	if fake.calls[1].name != "7d" || fake.calls[1].usedPercent != 0.77 {
		t.Errorf("7d call = %+v, want name=7d used=0.77", fake.calls[1])
	}
}

// TestQuotaCache_NoHeadersNoop verifies that missing headers are ignored.
func TestQuotaCache_NoHeadersNoop(t *testing.T) {
	fake := &fakeUsageCache{}
	qc := codexmw.NewQuotaCache(fake)

	qc.Apply(makeResp(map[string]string{}))

	if len(fake.calls) != 0 {
		t.Fatalf("expected no calls, got %d: %+v", len(fake.calls), fake.calls)
	}
}

// TestQuotaCache_MalformedNumeric verifies that an unparseable used-percent
// value causes the entry to be silently skipped.
func TestQuotaCache_MalformedNumeric(t *testing.T) {
	fake := &fakeUsageCache{}
	qc := codexmw.NewQuotaCache(fake)

	resetTime := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	resp := makeResp(map[string]string{
		"x-codex-5h-used":      "not-a-number",
		"x-codex-5h-resets-at": resetTime,
	})

	qc.Apply(resp)

	if len(fake.calls) != 0 {
		t.Fatalf("expected no calls on malformed numeric, got %d", len(fake.calls))
	}
}

// TestQuotaCache_MalformedTime verifies that an unparseable reset-time value
// causes the entry to be silently skipped.
func TestQuotaCache_MalformedTime(t *testing.T) {
	fake := &fakeUsageCache{}
	qc := codexmw.NewQuotaCache(fake)

	resp := makeResp(map[string]string{
		"x-codex-7d-used":      "0.5",
		"x-codex-7d-resets-at": "not-a-time",
	})

	qc.Apply(resp)

	if len(fake.calls) != 0 {
		t.Fatalf("expected no calls on malformed time, got %d", len(fake.calls))
	}
}

// TestQuotaCache_PartialHeaders verifies that only complete pairs
// (used + resets-at) produce a cache update.
func TestQuotaCache_PartialHeaders(t *testing.T) {
	fake := &fakeUsageCache{}
	qc := codexmw.NewQuotaCache(fake)

	resetTime := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	// 5h has both headers; 7d is missing resets-at.
	resp := makeResp(map[string]string{
		"x-codex-5h-used":      "0.1",
		"x-codex-5h-resets-at": resetTime,
		"x-codex-7d-used":      "0.9",
		// x-codex-7d-resets-at intentionally absent
	})

	qc.Apply(resp)

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call (only 5h), got %d", len(fake.calls))
	}
	if fake.calls[0].name != "5h" {
		t.Errorf("call name = %q, want 5h", fake.calls[0].name)
	}
}
