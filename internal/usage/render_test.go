package usage

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{999_999, "1000.0k"},
		{1_000_000, "1.0m"},
		{58_500_000, "58.5m"},
		{999_999_999, "1000.0m"},
		{1_000_000_000, "1.0b"},
		{84_000_000_000, "84.0b"},
	}
	for _, tt := range tests {
		got := formatTokens(tt.in)
		if got != tt.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0m"},
		{45 * time.Second, "0m"},
		{2 * time.Minute, "2m"},
		{61 * time.Minute, "1h 1m"},
		{8*24*time.Hour + 2*time.Hour + 12*time.Minute, "8d 2h 12m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.in)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatMonthDay(t *testing.T) {
	if got := formatMonthDay(time.Time{}); got != "—" {
		t.Errorf("zero time = %q, want '—'", got)
	}
	d := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	if got := formatMonthDay(d); got != "May 4" {
		t.Errorf("May 4 formatted = %q", got)
	}
}

func TestRenderHeatmap_NoActivity(t *testing.T) {
	agg := &Aggregate{DailyTokens: map[string]int64{}}
	var buf bytes.Buffer
	renderHeatmap(&buf, agg, time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), time.UTC)
	if !strings.Contains(buf.String(), "·") {
		t.Errorf("empty heatmap should still render dots, got %q", buf.String())
	}
}

func TestRenderHeatmap_WithActivity(t *testing.T) {
	today := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	agg := &Aggregate{
		DailyTokens: map[string]int64{
			today.Format("2006-01-02"):                   1_000_000,
			today.AddDate(0, 0, -7).Format("2006-01-02"): 500_000,
			today.AddDate(0, 0, -14).Format("2006-01-02"): 100_000,
			today.AddDate(0, 0, -21).Format("2006-01-02"): 50_000,
		},
	}
	var buf bytes.Buffer
	renderHeatmap(&buf, agg, today, time.UTC)
	if !strings.ContainsAny(buf.String(), "░▒▓█") {
		t.Errorf("expected at least one heatmap glyph, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "Less ░ ▒ ▓ █ More") {
		t.Errorf("legend missing: %q", buf.String())
	}
}

func TestRenderTotalsBlock(t *testing.T) {
	agg := &Aggregate{
		Total:          58_500_000,
		Read:           12_300_000,
		CachedRead:     41_200_000,
		Write:          4_800_000,
		CachedWrite:    200_000,
		FavoriteModel:  "Opus 4.7",
		Sessions:       392,
		LongestSession: 8*24*time.Hour + 2*time.Hour + 12*time.Minute,
		ActiveDays:     59,
		RangeDays:      64,
		LongestStreak:  38,
		CurrentStreak:  3,
		MostActiveDay:  time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	}
	var buf bytes.Buffer
	renderTotals(&buf, agg, "All time")
	out := buf.String()
	want := []string{
		"Favorite model: Opus 4.7",
		"Total tokens: 58.5m",
		"Read: 12.3m",
		"Cached read: 41.2m",
		"Write: 4.8m",
		"Cached write: 200.0k",
		"Sessions: 392",
		"Longest session: 8d 2h 12m",
		"Active days: 59/64",
		"Longest streak: 38 days",
		"Most active day: May 4",
		"Current streak: 3 days",
		"All time",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in totals block:\n%s", w, out)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	agg := &Aggregate{DailyTokens: map[string]int64{}}
	var buf bytes.Buffer
	Render(&buf, agg, RangeAll, time.Now().UTC(), time.UTC)
	if !strings.Contains(buf.String(), "No usage recorded yet") {
		t.Errorf("empty Render should show empty-state message, got: %q", buf.String())
	}
}

func TestRenderNilAgg(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, nil, RangeAll, time.Now().UTC(), time.UTC)
	if !strings.Contains(buf.String(), "No usage recorded yet") {
		t.Errorf("nil Render should show empty-state, got: %q", buf.String())
	}
}

func TestRenderFull(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	agg := &Aggregate{
		Total:         100,
		Read:          50,
		Write:         50,
		FavoriteModel: "Opus 4.7",
		Sessions:      1,
		ActiveDays:    1,
		RangeDays:     7,
		DailyTokens:   map[string]int64{now.Format("2006-01-02"): 100},
	}
	var buf bytes.Buffer
	Render(&buf, agg, Range7d, now, time.UTC)
	out := buf.String()
	if !strings.Contains(out, "Last 7 days") {
		t.Errorf("missing range label: %s", out)
	}
	if !strings.Contains(out, "Favorite model: Opus 4.7") {
		t.Errorf("missing favorite model: %s", out)
	}
}

func TestRangeLabel(t *testing.T) {
	tests := []struct {
		r    Range
		want string
	}{
		{RangeAll, "All time"},
		{Range7d, "Last 7 days"},
		{Range30d, "Last 30 days"},
	}
	for _, tt := range tests {
		if got := rangeLabel(tt.r); got != tt.want {
			t.Errorf("rangeLabel(%v) = %q, want %q", tt.r, got, tt.want)
		}
	}
}

func TestRenderJSON(t *testing.T) {
	agg := &Aggregate{Total: 100, Read: 50, Write: 50}
	var buf bytes.Buffer
	if err := RenderJSON(&buf, agg); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"Total": 100`) {
		t.Errorf("JSON output missing Total field: %s", buf.String())
	}
}

func TestQuartiles_Empty(t *testing.T) {
	q1, q2, q3 := quartiles(nil)
	if q1 != 0 || q2 != 0 || q3 != 0 {
		t.Errorf("empty quartiles = %d/%d/%d, want 0/0/0", q1, q2, q3)
	}
}

func TestBuildMonthHeader(t *testing.T) {
	// Start of Jan 2026 → spans Jan/Feb/Mar/...
	start := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC) // a Sunday
	hdr := buildMonthHeader(start, 52)
	// Should contain at least Jan and Feb labels somewhere.
	if !strings.Contains(hdr, "Jan") {
		t.Errorf("header missing Jan: %q", hdr)
	}
	if !strings.Contains(hdr, "Feb") {
		t.Errorf("header missing Feb: %q", hdr)
	}
}
