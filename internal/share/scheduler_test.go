package share

import (
	"math"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
)

func ptrFloat(f float64) *float64 { return &f }

func TestComputeFeasibilityBothPresent(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 20, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// left5h = 50, wait5h = 3600, left7d = 80, wait7d = 86400
	// = 50/3600 + 0.7 * 80/86400
	want := 50.0/3600 + 0.7*80.0/86400
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibility5hMissing(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "7d", Used: 20, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// 5h falls back: left=100, wait=1; 7d normal
	want := 100.0/1.0 + 0.7*80.0/86400
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibility7dMissing(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	want := 50.0/3600 + 0.7*100.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityBothMissing(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{Quotas: nil}
	got := computeFeasibility(info, now)
	want := 100.0/1.0 + 0.7*100.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityUsedOver100(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 150, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 0, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// left5h clamps to 0
	want := 0.0/3600 + 0.7*100.0/86400
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityResetInPast(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: now.Add(-time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 20, ResetsAt: now.Add(-time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	want := 50.0/1.0 + 0.7*80.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityParseFailure(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "5h", Used: 50, ResetsAt: "not-a-timestamp"},
			{Name: "7d", Used: 20, ResetsAt: ""},
		},
	}
	got := computeFeasibility(info, now)
	want := 50.0/1.0 + 0.7*80.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeFeasibilityIgnoresModelExtras(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	info := &oauth.UsageInfo{
		Quotas: []oauth.Quota{
			{Name: "7d/sonnet-4-5", Used: 10, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
			{Name: "7d/opus-4-7", Used: 5, ResetsAt: now.Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}
	got := computeFeasibility(info, now)
	// Both unscoped windows missing → both fallbacks
	want := 100.0/1.0 + 0.7*100.0/1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLookupQuota(t *testing.T) {
	qs := []oauth.Quota{
		{Name: "5h", Used: 1},
		{Name: "7d", Used: 2},
		{Name: "7d/sonnet", Used: 3},
	}
	if got := lookupQuota(qs, "5h"); got == nil || got.Used != 1 {
		t.Errorf("lookup 5h failed: %+v", got)
	}
	if got := lookupQuota(qs, "7d"); got == nil || got.Used != 2 {
		t.Errorf("lookup 7d failed: %+v", got)
	}
	if got := lookupQuota(qs, "missing"); got != nil {
		t.Errorf("lookup missing returned %+v, want nil", got)
	}
}
