package usage

import (
	"os"
	"testing"
	"time"
)

func writeRecords(t *testing.T, sid string, recs []Record) {
	t.Helper()
	if err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(SessionPath(sid), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
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

func dayAt(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

func TestAggregate_Totals(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000a1", []Record{
		{TS: dayAt(2026, 5, 1), Model: "claude-opus-4-7-20251217", In: 100, Out: 200, CR: 50, CW: 10, Stream: true},
		{TS: dayAt(2026, 5, 1), Model: "claude-opus-4-7-20251217", In: 200, Out: 300, CR: 100, CW: 20, Stream: true},
	})
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000a2", []Record{
		{TS: dayAt(2026, 5, 2), Model: "claude-sonnet-4-6-20250101", In: 50, Out: 50, Stream: true},
	})

	agg, err := LoadAggregate(FilterAll(), time.UTC)
	if err != nil {
		t.Fatalf("LoadAggregate: %v", err)
	}
	if agg.Read != 350 || agg.Write != 550 || agg.CachedRead != 150 || agg.CachedWrite != 30 {
		t.Errorf("totals = read=%d write=%d cr=%d cw=%d, want 350/550/150/30",
			agg.Read, agg.Write, agg.CachedRead, agg.CachedWrite)
	}
	if agg.Total != 350+550+150+30 {
		t.Errorf("Total = %d, want %d", agg.Total, 350+550+150+30)
	}
	if agg.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2", agg.Sessions)
	}
	if agg.FavoriteModel != "Opus 4.7" {
		t.Errorf("FavoriteModel = %q, want Opus 4.7", agg.FavoriteModel)
	}
}

func TestAggregate_FavoriteModel_AlphabeticalTiebreak(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000b1", []Record{
		{TS: dayAt(2026, 5, 1), Model: "claude-opus-4-7-20251217", In: 100, Out: 100},
		{TS: dayAt(2026, 5, 1), Model: "claude-sonnet-4-6-20250101", In: 100, Out: 100},
	})
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	// Equal tokens; alphabetical on stripped IDs:
	// "claude-opus-4-7" < "claude-sonnet-4-6"
	if agg.FavoriteModel != "Opus 4.7" {
		t.Errorf("tiebreak: FavoriteModel = %q, want Opus 4.7", agg.FavoriteModel)
	}
}

func TestAggregate_Streaks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	recs := []Record{
		{TS: now, In: 1, Out: 1},
		{TS: now.AddDate(0, 0, -1), In: 1, Out: 1},
		{TS: now.AddDate(0, 0, -2), In: 1, Out: 1},
		{TS: now.AddDate(0, 0, -5), In: 1, Out: 1},
		{TS: now.AddDate(0, 0, -6), In: 1, Out: 1},
	}
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000c1", recs)
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	if agg.LongestStreak != 3 {
		t.Errorf("LongestStreak = %d, want 3", agg.LongestStreak)
	}
	if agg.CurrentStreak != 3 {
		t.Errorf("CurrentStreak = %d, want 3", agg.CurrentStreak)
	}
	if agg.ActiveDays != 5 {
		t.Errorf("ActiveDays = %d, want 5", agg.ActiveDays)
	}
}

func TestAggregate_CurrentStreakZeroWhenTodayInactive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000c2", []Record{
		{TS: now.AddDate(0, 0, -1), In: 1, Out: 1},
		{TS: now.AddDate(0, 0, -2), In: 1, Out: 1},
	})
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	if agg.CurrentStreak != 0 {
		t.Errorf("CurrentStreak = %d, want 0 (today inactive)", agg.CurrentStreak)
	}
	if agg.LongestStreak != 2 {
		t.Errorf("LongestStreak = %d, want 2", agg.LongestStreak)
	}
}

func TestAggregate_StreakIgnoresRangeFilter(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	var recs []Record
	for d := 0; d < 30; d++ {
		recs = append(recs, Record{TS: now.AddDate(0, 0, -d), In: 1, Out: 1})
	}
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000c3", recs)
	agg, _ := LoadAggregate(Filter{Range: Range7d}, time.UTC)
	if agg.LongestStreak != 30 {
		t.Errorf("LongestStreak under --range 7d = %d, want 30 (streaks span unfiltered history)", agg.LongestStreak)
	}
	if agg.CurrentStreak != 30 {
		t.Errorf("CurrentStreak under --range 7d = %d, want 30", agg.CurrentStreak)
	}
}

func TestAggregate_RangeFiltersTotals(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000d1", []Record{
		{TS: now.AddDate(0, 0, -2), In: 100, Out: 100, Stream: true},
		{TS: now.AddDate(0, 0, -10), In: 1000, Out: 1000, Stream: true},
	})
	agg, _ := LoadAggregate(Filter{Range: Range7d}, time.UTC)
	if agg.Read != 100 || agg.Write != 100 {
		t.Errorf("range 7d totals = r=%d w=%d, want 100/100 (older record filtered)", agg.Read, agg.Write)
	}
	aggAll, _ := LoadAggregate(FilterAll(), time.UTC)
	if aggAll.Read != 1100 {
		t.Errorf("range all totals.read = %d, want 1100", aggAll.Read)
	}
}

func TestAggregate_Range30dFilter(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-00000000d30a", []Record{
		{TS: now.AddDate(0, 0, -15), In: 50, Out: 50, Stream: true},
		{TS: now.AddDate(0, 0, -45), In: 5000, Out: 5000, Stream: true},
	})
	agg, _ := LoadAggregate(Filter{Range: Range30d}, time.UTC)
	if agg.Read != 50 || agg.Write != 50 {
		t.Errorf("range 30d: r=%d w=%d, want 50/50", agg.Read, agg.Write)
	}
	if agg.RangeDays != 30 {
		t.Errorf("RangeDays = %d, want 30", agg.RangeDays)
	}
}

func TestAggregate_SessionFilter(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sidA := "5f2c8c4e-1234-4567-8abc-0000000000e1"
	sidB := "5f2c8c4e-1234-4567-8abc-0000000000e2"
	writeRecords(t, sidA, []Record{{TS: dayAt(2026, 5, 1), In: 10}})
	writeRecords(t, sidB, []Record{{TS: dayAt(2026, 5, 1), In: 20}})
	agg, _ := LoadAggregate(Filter{Range: RangeAll, SessionID: sidB}, time.UTC)
	if agg.Read != 20 {
		t.Errorf("Read = %d, want 20 (filtered to sidB)", agg.Read)
	}
	if agg.Sessions != 1 {
		t.Errorf("Sessions = %d, want 1", agg.Sessions)
	}
}

func TestAggregate_LongestSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sidA := "5f2c8c4e-1234-4567-8abc-0000000000f1"
	writeRecords(t, sidA, []Record{
		{TS: dayAt(2026, 5, 1), In: 1},
		{TS: dayAt(2026, 5, 1).Add(2 * time.Hour), In: 1},
		{TS: dayAt(2026, 5, 1).Add(5 * time.Hour), In: 1},
	})
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	if agg.LongestSession != 5*time.Hour {
		t.Errorf("LongestSession = %v, want 5h", agg.LongestSession)
	}
}

func TestAggregate_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	agg, err := LoadAggregate(FilterAll(), time.UTC)
	if err != nil {
		t.Fatalf("LoadAggregate empty: %v", err)
	}
	if agg.Total != 0 || agg.Sessions != 0 {
		t.Errorf("empty agg = %+v", agg)
	}
}

func TestAggregate_DailyTokensMap(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000g1", []Record{
		{TS: dayAt(2026, 5, 1), In: 100, Out: 100},
		{TS: dayAt(2026, 5, 1), In: 50, Out: 50},
		{TS: dayAt(2026, 5, 3), In: 200, Out: 200},
	})
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	if got := agg.DailyTokens["2026-05-01"]; got != 300 {
		t.Errorf("daily 5/1 = %d, want 300", got)
	}
	if got := agg.DailyTokens["2026-05-03"]; got != 400 {
		t.Errorf("daily 5/3 = %d, want 400", got)
	}
	if _, ok := agg.DailyTokens["2026-05-02"]; ok {
		t.Errorf("5/2 should not appear in DailyTokens (zero day)")
	}
}

func TestAggregate_MostActiveDay(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-0000000000h1", []Record{
		{TS: dayAt(2026, 5, 1), In: 10, Out: 10},
		{TS: dayAt(2026, 5, 4), In: 1000, Out: 1000},
		{TS: dayAt(2026, 5, 7), In: 100, Out: 100},
	})
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	want := dayAt(2026, 5, 4).Truncate(24 * time.Hour)
	if !agg.MostActiveDay.Equal(want) {
		t.Errorf("MostActiveDay = %v, want %v", agg.MostActiveDay, want)
	}
}

// LoadAggregate must surface a real ReadDir error (i.e. anything other
// than "directory does not exist", which is the empty-state).
func TestAggregate_ReadDirErrorPropagates(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("permission semantics differ on Windows")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Create ~/.ccm/usage as a regular FILE so ReadDir on it returns
	// ENOTDIR (not ErrNotExist).
	if err := os.MkdirAll(tmp+"/.ccm", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp+"/.ccm/usage", []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAggregate(FilterAll(), time.UTC); err == nil {
		t.Fatalf("expected error when usage path is not a directory")
	}
}

func TestAggregate_SkipsSubdirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	// Create a subdir inside ~/.ccm/usage/ — must be ignored.
	if err := os.Mkdir(Dir()+"/sub.ndjson", 0700); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-000000000099", []Record{
		{TS: dayAt(2026, 5, 1), In: 7},
	})
	agg, err := LoadAggregate(FilterAll(), time.UTC)
	if err != nil {
		t.Fatalf("LoadAggregate: %v", err)
	}
	if agg.Read != 7 {
		t.Errorf("Read = %d, want 7 (subdir should be skipped)", agg.Read)
	}
}

func TestAggregate_SkipsNonNDJSONFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Dir()+"/notes.txt", []byte("not ours"), 0600); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, "5f2c8c4e-1234-4567-8abc-000000000098", []Record{
		{TS: dayAt(2026, 5, 1), In: 3},
	})
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	if agg.Read != 3 {
		t.Errorf("Read = %d, want 3 (non-ndjson skipped)", agg.Read)
	}
}

// Empty modelTokens map → pickFavorite returns "" (no records yet).
func TestAggregate_EmptyFavoriteModel(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	agg, _ := LoadAggregate(FilterAll(), time.UTC)
	if agg.FavoriteModel != "" {
		t.Errorf("FavoriteModel = %q, want empty", agg.FavoriteModel)
	}
}
