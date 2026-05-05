package share

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// parseRatelimitHeaders extracts the per-window quota state from an
// Anthropic /v1/messages response. Empirically observed headers:
//
//	Anthropic-Ratelimit-Unified-5h-Utilization: 0.20    (float 0.0..1.0)
//	Anthropic-Ratelimit-Unified-5h-Reset:       1777983000  (Unix sec)
//	Anthropic-Ratelimit-Unified-7d-Utilization: 0.74
//	Anthropic-Ratelimit-Unified-7d-Reset:       1778216400
//
// Per-window leniency: each window contributes a Quota independently.
// Windows with missing or unparseable fields are silently dropped.
// Returns *UsageInfo when at least one window parsed; nil otherwise.
//
// Forward-compat: future windows Anthropic might add (1d, 24h, etc.)
// are unknown to the parser and silently ignored — known windows
// still contribute.
//
// The values reported here measure a different ceiling than
// /api/oauth/usage (sliding-rate-limit window vs. session-quota
// cap). They are not directly comparable but the feasibility formula
// is monotonic in "less used = better" so the rotation remains
// directionally correct after a header refresh. Documented trade-off:
// see docs/superpowers/specs/2026-05-05-usage-cache-design.md.
func parseRatelimitHeaders(h http.Header) *oauth.UsageInfo {
	out := &oauth.UsageInfo{}
	if q := parseUnifiedWindow(h, "5h"); q != nil {
		out.Quotas = append(out.Quotas, *q)
	}
	if q := parseUnifiedWindow(h, "7d"); q != nil {
		out.Quotas = append(out.Quotas, *q)
	}
	if len(out.Quotas) == 0 {
		return nil
	}
	return out
}

func parseUnifiedWindow(h http.Header, window string) *oauth.Quota {
	util := h.Get("Anthropic-Ratelimit-Unified-" + window + "-Utilization")
	reset := h.Get("Anthropic-Ratelimit-Unified-" + window + "-Reset")
	if util == "" || reset == "" {
		return nil
	}
	u, err := strconv.ParseFloat(util, 64)
	if err != nil {
		return nil
	}
	sec, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return nil
	}
	return &oauth.Quota{
		Name:     window,
		Used:     u * 100, // header is 0..1; Quota.Used is 0..100
		ResetsAt: time.Unix(sec, 0).UTC().Format(time.RFC3339),
	}
}

// parseRatelimitHeadersFn is the test seam over parseRatelimitHeaders.
// Production wires it directly to the function; tests can swap to a
// counter or fail-on-call instrumentation to verify the gate at
// proxy.go's ModifyResponse hook.
var parseRatelimitHeadersFn = parseRatelimitHeaders
