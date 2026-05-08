package middleware

import (
	"net/http"
	"strconv"
	"time"
)

// UsageCache is the indirection used by QuotaCache to persist per-cred
// usage telemetry. The real cache lives in internal/share/credstate;
// tests pass fakes.
type UsageCache interface {
	Update(name string, usedPercent float64, resetAt time.Time)
}

// QuotaCache parses x-codex-{5h,7d}-* response headers and updates the
// usage cache. Spec §6.8.
type QuotaCache struct {
	cache UsageCache
}

// NewQuotaCache constructs a QuotaCache backed by the given UsageCache.
func NewQuotaCache(c UsageCache) *QuotaCache {
	return &QuotaCache{cache: c}
}

// Apply reads codex usage headers off resp and writes them to the cache.
// No-op if headers are absent or unparseable.
func (q *QuotaCache) Apply(resp *http.Response) {
	for _, name := range []string{"5h", "7d"} {
		usedRaw := resp.Header.Get("x-codex-" + name + "-used")
		resetRaw := resp.Header.Get("x-codex-" + name + "-resets-at")
		if usedRaw == "" || resetRaw == "" {
			continue
		}
		used, err := strconv.ParseFloat(usedRaw, 64)
		if err != nil {
			continue
		}
		resetAt, err := time.Parse(time.RFC3339, resetRaw)
		if err != nil {
			continue
		}
		q.cache.Update(name, used, resetAt)
	}
}
