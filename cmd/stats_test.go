package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/usage"
	"github.com/spf13/cobra"
)

func setupStatsHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

func writeUsageRecords(t *testing.T, sid string, recs []usage.Record) {
	t.Helper()
	if err := usage.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(usage.SessionPath(sid), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range recs {
		b, _ := r.Marshal()
		f.Write(b)
		f.Write([]byte("\n"))
	}
}

// runStatsForTest constructs a fresh cobra.Command, runs runStats
// with the given flag values, captures stdout, and returns the
// (output, error). Avoids Cobra's os.Args parsing entirely.
func runStatsForTest(t *testing.T, rng, session string, asJSON bool) (string, error) {
	t.Helper()
	statsRange = rng
	statsSession = session
	statsJSON = asJSON
	c := &cobra.Command{}
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	err := runStats(c, nil)
	return buf.String(), err
}

func TestStats_DefaultRangeAll(t *testing.T) {
	setupStatsHome(t)
	now := time.Now().UTC()
	writeUsageRecords(t, "5f2c8c4e-1234-4567-8abc-0123456789ab", []usage.Record{
		{TS: now, Model: "claude-opus-4-7-20251217", In: 100, Out: 200, CR: 50, CW: 10, Stream: true},
	})
	out, err := runStatsForTest(t, "all", "", false)
	if err != nil {
		t.Fatalf("runStats: %v", err)
	}
	if !strings.Contains(out, "All time") {
		t.Errorf("missing range label: %q", out)
	}
	if !strings.Contains(out, "Total tokens:") {
		t.Errorf("missing totals: %q", out)
	}
}

func TestStats_JSON(t *testing.T) {
	setupStatsHome(t)
	now := time.Now().UTC()
	writeUsageRecords(t, "5f2c8c4e-1234-4567-8abc-0123456789ab", []usage.Record{
		{TS: now, Model: "claude-opus-4-7-20251217", In: 100, Out: 200},
	})
	out, err := runStatsForTest(t, "all", "", true)
	if err != nil {
		t.Fatalf("runStats: %v", err)
	}
	var dump map[string]interface{}
	if err := json.Unmarshal([]byte(out), &dump); err != nil {
		t.Fatalf("json output not valid: %v\n%s", err, out)
	}
	if dump["Total"] == nil {
		t.Errorf("Total field missing in JSON dump")
	}
}

func TestStats_RangeFlag(t *testing.T) {
	setupStatsHome(t)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	writeUsageRecords(t, "5f2c8c4e-1234-4567-8abc-0123456789ab", []usage.Record{
		{TS: now.AddDate(0, 0, -2), In: 100, Out: 100},
		{TS: now.AddDate(0, 0, -10), In: 1000, Out: 1000},
	})
	out, err := runStatsForTest(t, "7d", "", false)
	if err != nil {
		t.Fatalf("runStats: %v", err)
	}
	if !strings.Contains(out, "Last 7 days") {
		t.Errorf("missing range label: %q", out)
	}
	totalLine := regexp.MustCompile(`(?m)^[ \t]+Total tokens:\s+(\S+)\s*$`)
	m := totalLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("could not find 'Total tokens:' line: %q", out)
	}
	if m[1] != "200" {
		t.Errorf("Total tokens = %q under --range 7d, want 200 (older record filtered)", m[1])
	}
}

func TestStats_Range30dFlag(t *testing.T) {
	setupStatsHome(t)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	writeUsageRecords(t, "5f2c8c4e-1234-4567-8abc-0123456789ab", []usage.Record{
		{TS: now.AddDate(0, 0, -15), In: 1, Out: 1},
	})
	out, err := runStatsForTest(t, "30d", "", false)
	if err != nil {
		t.Fatalf("runStats: %v", err)
	}
	if !strings.Contains(out, "Last 30 days") {
		t.Errorf("missing range label: %q", out)
	}
}

func TestStats_InvalidRange(t *testing.T) {
	setupStatsHome(t)
	_, err := runStatsForTest(t, "999d", "", false)
	if err == nil {
		t.Errorf("expected error for invalid range")
	}
}

func TestStats_EmptyDir(t *testing.T) {
	setupStatsHome(t)
	out, err := runStatsForTest(t, "all", "", false)
	if err != nil {
		t.Fatalf("runStats: %v", err)
	}
	if !strings.Contains(out, "No usage recorded yet") {
		t.Errorf("expected empty-state message, got: %q", out)
	}
}

func TestStats_SessionFilter(t *testing.T) {
	setupStatsHome(t)
	now := time.Now().UTC()
	writeUsageRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000aa", []usage.Record{
		{TS: now, In: 10, Out: 10},
	})
	writeUsageRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000bb", []usage.Record{
		{TS: now, In: 100, Out: 100},
	})
	out, err := runStatsForTest(t, "all", "5f2c8c4e-1234-4567-8abc-0000000000bb", false)
	if err != nil {
		t.Fatalf("runStats: %v", err)
	}
	totalLine := regexp.MustCompile(`(?m)^[ \t]+Total tokens:\s+(\S+)\s*$`)
	m := totalLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("could not find 'Total tokens:' line: %q", out)
	}
	if m[1] != "200" {
		t.Errorf("session filter: Total tokens = %q, want 200", m[1])
	}
}

func TestStats_LoadAggregateError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("permission semantics differ on Windows")
	}
	tmp := setupStatsHome(t)
	if err := os.MkdirAll(tmp+"/.ccm", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp+"/.ccm/usage", []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runStatsForTest(t, "all", "", false); err == nil {
		t.Errorf("expected LoadAggregate error to surface")
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		in      string
		want    usage.Range
		wantErr bool
	}{
		{"all", usage.RangeAll, false},
		{"7d", usage.Range7d, false},
		{"30d", usage.Range30d, false},
		{"junk", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseRange(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseRange(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseRange(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
