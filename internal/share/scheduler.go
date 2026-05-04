package share

import (
	"math"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

// computeFeasibility returns the rotation score for a single
// credential's usage snapshot. See the design doc for the formula
// and edge-case rules (clamps + missing-window fallbacks).
func computeFeasibility(info *oauth.UsageInfo, now time.Time) float64 {
	left5h, wait5h := windowInputs(lookupQuota(info.Quotas, "5h"), now)
	left7d, wait7d := windowInputs(lookupQuota(info.Quotas, "7d"), now)
	return left5h/wait5h + 0.7*left7d/wait7d
}

// windowInputs returns (left%, wait_seconds) for a single quota
// window, applying clamps and best-case fallbacks for nil/missing.
func windowInputs(q *oauth.Quota, now time.Time) (float64, float64) {
	if q == nil {
		return 100, 1
	}
	left := math.Max(0, math.Min(100, 100-q.Used))
	wait := math.Max(1, secondsUntil(q.ResetsAt, now))
	return left, wait
}

func secondsUntil(stamp string, now time.Time) float64 {
	if stamp == "" {
		return 1
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return 1
		}
	}
	return t.Sub(now).Seconds()
}

func lookupQuota(qs []oauth.Quota, name string) *oauth.Quota {
	for i := range qs {
		if qs[i].Name == name {
			return &qs[i]
		}
	}
	return nil
}
