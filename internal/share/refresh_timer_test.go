package share

import (
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func credWithExpiry(at time.Time) *store.Credential {
	return &store.Credential{
		ID: "00000000-0000-0000-0000-000000000001",
		ClaudeAiOauth: store.OAuthTokens{
			ExpiresAt: at.UnixMilli(),
		},
	}
}

func TestNextRefreshDelayAlreadyExpired(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	cred := credWithExpiry(now.Add(-time.Hour))
	got := nextRefreshDelay(cred, now, func() time.Duration { return 30 * time.Second })
	if got != 0 {
		t.Errorf("got %v, want 0 (jitter clamped to delay/4 == 0)", got)
	}
}

func TestNextRefreshDelay30MinFuture(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	cred := credWithExpiry(now.Add(30 * time.Minute))
	jitter := 30 * time.Second
	got := nextRefreshDelay(cred, now, func() time.Duration { return jitter })
	// target = 25 min; delay = 25 min; jitter offered 30s; cap min(60s, delay/4=6.25m)=60s; final = 30s
	want := 25*time.Minute + 30*time.Second
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNextRefreshDelayJitterClampedByDelay(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	// Token expires in 5m10s ⇒ target 10s away; delay/4 = 2.5s
	cred := credWithExpiry(now.Add(5*time.Minute + 10*time.Second))
	got := nextRefreshDelay(cred, now, func() time.Duration { return 60 * time.Second })
	want := 10*time.Second + 2500*time.Millisecond
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNextRefreshDelayNegativeJitter(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	cred := credWithExpiry(now.Add(time.Hour))
	got := nextRefreshDelay(cred, now, func() time.Duration { return -42 * time.Second })
	want := 55 * time.Minute
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNextRefreshDelayJitterCappedAt60s(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	// Expires in 24 hours ⇒ delay = 23h55m; delay/4 ≈ 5h59m; jitter cap is min(60s, that) = 60s
	cred := credWithExpiry(now.Add(24 * time.Hour))
	got := nextRefreshDelay(cred, now, func() time.Duration { return 5 * time.Minute })
	want := 23*time.Hour + 55*time.Minute + 60*time.Second
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
