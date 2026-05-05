package share

import (
	"net/http"
	"strings"
	"testing"
)

func TestParseRatelimitHeaders_Happy(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.20")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.74")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1778216400")

	info := parseRatelimitHeaders(h)
	if info == nil {
		t.Fatal("parseRatelimitHeaders returned nil for fully-populated headers")
	}
	if len(info.Quotas) != 2 {
		t.Fatalf("got %d quotas, want 2", len(info.Quotas))
	}
	got := map[string]float64{}
	gotResets := map[string]string{}
	for _, q := range info.Quotas {
		got[q.Name] = q.Used
		gotResets[q.Name] = q.ResetsAt
	}
	if got["5h"] != 20 {
		t.Errorf("5h Used = %v, want 20", got["5h"])
	}
	if got["7d"] != 74 {
		t.Errorf("7d Used = %v, want 74", got["7d"])
	}
	if !strings.HasPrefix(gotResets["5h"], "2026-") {
		t.Errorf("5h ResetsAt = %q, want RFC3339 starting 2026-", gotResets["5h"])
	}
	if !strings.HasPrefix(gotResets["7d"], "2026-") {
		t.Errorf("7d ResetsAt = %q, want RFC3339 starting 2026-", gotResets["7d"])
	}
}

func TestParseRatelimitHeaders_Missing7d(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.50")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
	info := parseRatelimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil with 5h only (per-window leniency)")
	}
	if len(info.Quotas) != 1 || info.Quotas[0].Name != "5h" {
		t.Fatalf("quotas = %+v, want single 5h", info.Quotas)
	}
}

func TestParseRatelimitHeaders_Missing5h(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.50")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1777983000")
	info := parseRatelimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil with 7d only")
	}
	if len(info.Quotas) != 1 || info.Quotas[0].Name != "7d" {
		t.Fatalf("quotas = %+v, want single 7d", info.Quotas)
	}
}

func TestParseRatelimitHeaders_BadUtilizationOneWindow(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "not-a-number")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.10")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1778216400")
	info := parseRatelimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil with 7d salvaged")
	}
	if len(info.Quotas) != 1 || info.Quotas[0].Name != "7d" {
		t.Fatalf("quotas = %+v, want single 7d (5h dropped due to bad util)", info.Quotas)
	}
}

func TestParseRatelimitHeaders_BadResetOneWindow(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.50")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "not-an-int")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.20")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1778216400")
	info := parseRatelimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil with 7d salvaged")
	}
	if len(info.Quotas) != 1 || info.Quotas[0].Name != "7d" {
		t.Fatalf("quotas = %+v, want single 7d", info.Quotas)
	}
}

func TestParseRatelimitHeaders_AllWindowsBad(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "garbage")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.20")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "garbage")
	info := parseRatelimitHeaders(h)
	if info != nil {
		t.Fatalf("expected nil when both windows have bad data, got %+v", info)
	}
}

func TestParseRatelimitHeaders_NoHeaders(t *testing.T) {
	if info := parseRatelimitHeaders(http.Header{}); info != nil {
		t.Fatalf("expected nil for empty headers, got %+v", info)
	}
}

func TestParseRatelimitHeaders_OverUtilization(t *testing.T) {
	// Anthropic returns >1.0 when over-quota; clamping is the
	// formula's job, not the parser's.
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1.5")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
	info := parseRatelimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil")
	}
	if info.Quotas[0].Used != 150 {
		t.Errorf("Used = %v, want 150 (parser does not clamp)", info.Quotas[0].Used)
	}
}

func TestParseRatelimitHeaders_FutureWindowIgnored(t *testing.T) {
	// Hypothetical future window the parser doesn't know about.
	// Should be silently ignored; known windows still emit.
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-1d-Utilization", "0.99")
	h.Set("Anthropic-Ratelimit-Unified-1d-Reset", "1777983000")
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.20")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1777983000")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.10")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1778216400")
	info := parseRatelimitHeaders(h)
	if info == nil {
		t.Fatal("expected non-nil")
	}
	if len(info.Quotas) != 2 {
		t.Fatalf("got %d quotas, want 2 (1d should be ignored)", len(info.Quotas))
	}
	for _, q := range info.Quotas {
		if q.Name == "1d" {
			t.Errorf("1d should be ignored; found in output: %+v", q)
		}
	}
}
