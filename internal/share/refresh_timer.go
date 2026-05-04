package share

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
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

// runRefreshTimer is the per-credential goroutine that proactively
// refreshes a token shortly before it expires. The loop runs until
// done is closed.
func runRefreshTimer(state poolEntryState, c clock, jitter func() time.Duration, done <-chan struct{}) {
	for {
		delay := nextRefreshDelay(state.credPtr(), c.Now(), jitter)
		timer := c.NewTimer(delay)
		select {
		case <-done:
			timer.Stop()
			return
		case <-timer.C():
		}
		if _, err := state.Fresh(); err != nil {
			fmt.Fprintf(errLog(), "ccm share: refresh failed for %s(%s): %v\n",
				state.credName(), shortID(state.credID()), err)
			// Constant 30s back-off after a failed refresh — see
			// design doc §"Per-credential refresh timers".
			boTimer := c.NewTimer(refreshErrorBackoff)
			select {
			case <-done:
				boTimer.Stop()
				return
			case <-boTimer.C():
			}
		}
	}
}

// jitterReader is the test seam for crypto/rand. Defaults to
// rand.Reader; tests stub it.
var jitterReader io.Reader = rand.Reader

// jitterFn returns a random non-negative duration up to jitterCap,
// using crypto/rand. On RNG failure (kernel broken — unreachable in
// practice) it returns 0 and emits one log line per process.
var rngFailureLogged sync.Once

func jitterFn() time.Duration {
	var b [8]byte
	if _, err := io.ReadFull(jitterReader, b[:]); err != nil {
		// coverage: unreachable in production — kernel RNG failure.
		rngFailureLogged.Do(func() {
			fmt.Fprintf(errLog(), "ccm share: warning: crypto/rand failed, using deterministic refresh jitter: %v\n", err)
		})
		return 0
	}
	n := binary.LittleEndian.Uint64(b[:])
	return time.Duration(n % uint64(jitterCap))
}
