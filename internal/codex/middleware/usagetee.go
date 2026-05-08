package middleware

import "sync"

// UsageEvent is a single recorded usage record.
type UsageEvent struct {
	InputTokens          int
	OutputTokens         int
	CacheReadInputTokens int
}

// UsageTee is a ring buffer of recent usage events. Used by ccm status
// and ccm serve to display per-session usage history. Thread-safe.
type UsageTee struct {
	mu      sync.Mutex
	buf     []UsageEvent
	cap     int
	idx     int
	wrapped bool
}

// NewUsageTee constructs a UsageTee with the given capacity.
func NewUsageTee(capacity int) *UsageTee {
	return &UsageTee{buf: make([]UsageEvent, capacity), cap: capacity}
}

// Record appends an event; oldest is evicted when capacity is reached.
func (u *UsageTee) Record(ev UsageEvent) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.buf[u.idx] = ev
	u.idx = (u.idx + 1) % u.cap
	if u.idx == 0 {
		u.wrapped = true
	}
}

// Snapshot returns a copy of the buffer in oldest-first order.
func (u *UsageTee) Snapshot() []UsageEvent {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.wrapped {
		out := make([]UsageEvent, u.idx)
		copy(out, u.buf[:u.idx])
		return out
	}
	out := make([]UsageEvent, u.cap)
	copy(out, u.buf[u.idx:])
	copy(out[u.cap-u.idx:], u.buf[:u.idx])
	return out
}
