package share

import (
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// refreshExpiryMargin is how far before ExpiresAt a per-credential
// refresh timer wakes up. Mirrors store.Credential.IsExpiringSoon's
// 5-minute threshold so the timer and per-request opportunistic
// refresh agree on what "soon" means.
const refreshExpiryMargin = 5 * time.Minute

// refreshErrorBackoff is the constant floor applied when a refresh
// errors. The loop re-arms on this delay regardless of ExpiresAt.
// We deliberately do not exponentially back off — a recovering
// credential should be picked up promptly.
const refreshErrorBackoff = 30 * time.Second

// jitterCap is the absolute ceiling on jitter added to a refresh
// delay. Combined with delay/4, prevents short-window pathology
// (e.g. a 30s-remaining token getting a 60s jitter delay).
const jitterCap = 60 * time.Second

// nextRefreshDelay computes when a refresh-timer goroutine should
// next wake up.
//
// Pure function — no side effects, uses the supplied jitter()
// function so tests can inject deterministic values.
func nextRefreshDelay(cred *store.Credential, now time.Time, jitter func() time.Duration) time.Duration {
	target := time.UnixMilli(cred.ClaudeAiOauth.ExpiresAt).Add(-refreshExpiryMargin)
	delay := target.Sub(now)
	if delay < 0 {
		delay = 0
	}
	j := jitter()
	if j > jitterCap {
		j = jitterCap
	}
	if delay > 0 && j > delay/4 {
		j = delay / 4
	}
	if delay == 0 {
		j = 0
	}
	if j < 0 {
		j = 0
	}
	return delay + j
}
