package usage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Range is the metrics filter window.
type Range int

const (
	RangeAll Range = iota
	Range7d
	Range30d
)

// Filter selects which records contribute to the metrics block.
// Streaks always span the full unfiltered history regardless of Filter.Range.
type Filter struct {
	Range     Range
	SessionID string // empty = all sessions
}

// FilterAll is the default (all-time, all-sessions).
func FilterAll() Filter { return Filter{Range: RangeAll} }

// Aggregate is the rendered output of LoadAggregate.
type Aggregate struct {
	Total          int64
	Read           int64
	CachedRead     int64
	Write          int64
	CachedWrite    int64
	FavoriteModel  string
	Sessions       int
	LongestSession time.Duration
	ActiveDays     int
	RangeDays      int
	LongestStreak  int
	CurrentStreak  int
	MostActiveDay  time.Time
	DailyTokens    map[string]int64 // YYYY-MM-DD → token sum (full unfiltered history; for heatmap)
}

// LoadAggregate reads ~/.ccm/usage/, applies filter, and computes
// metrics. tz is the time zone used for civil-day bucketing.
func LoadAggregate(f Filter, tz *time.Location) (*Aggregate, error) {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	all := make(map[string][]Record)
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".ndjson") {
			continue
		}
		sid := strings.TrimSuffix(ent.Name(), ".ndjson")
		recs, _ := LoadFile(filepath.Join(dir, ent.Name()))
		all[sid] = recs
	}

	now := time.Now().In(tz)
	cutoff := time.Time{}
	switch f.Range {
	case Range7d:
		cutoff = now.Add(-7 * 24 * time.Hour)
	case Range30d:
		cutoff = now.Add(-30 * 24 * time.Hour)
	}

	agg := &Aggregate{}
	modelTokens := map[string]int64{}
	dailyAll := map[string]int64{}
	allDayKeys := map[string]struct{}{}
	rangeDayKeys := map[string]struct{}{}
	mostActiveTokens := int64(-1)
	sessions := 0

	for sid, recs := range all {
		if f.SessionID != "" && sid != f.SessionID {
			continue
		}
		var sessionMin, sessionMax time.Time
		var contributedToWindow bool

		for _, r := range recs {
			localDay := r.TS.In(tz).Format("2006-01-02")
			rowTotal := r.In + r.Out + r.CR + r.CW
			dailyAll[localDay] += rowTotal
			allDayKeys[localDay] = struct{}{}

			if cutoff.IsZero() || !r.TS.Before(cutoff) {
				agg.Read += r.In
				agg.CachedRead += r.CR
				agg.Write += r.Out
				agg.CachedWrite += r.CW
				agg.Total += rowTotal
				modelTokens[NormalizeModelID(r.Model)] += rowTotal
				rangeDayKeys[localDay] = struct{}{}
				if rowTotal > mostActiveTokens {
					mostActiveTokens = rowTotal
					y, m, d := r.TS.In(tz).Date()
					agg.MostActiveDay = time.Date(y, m, d, 0, 0, 0, 0, tz)
				}
				contributedToWindow = true
			}

			if sessionMin.IsZero() || r.TS.Before(sessionMin) {
				sessionMin = r.TS
			}
			if sessionMax.IsZero() || r.TS.After(sessionMax) {
				sessionMax = r.TS
			}
		}

		if contributedToWindow {
			sessions++
			if span := sessionMax.Sub(sessionMin); span > agg.LongestSession {
				agg.LongestSession = span
			}
		}
	}

	agg.Sessions = sessions
	agg.ActiveDays = len(rangeDayKeys)
	agg.DailyTokens = dailyAll
	switch f.Range {
	case Range7d:
		agg.RangeDays = 7
	case Range30d:
		agg.RangeDays = 30
	default:
		if len(allDayKeys) > 0 {
			var earliest time.Time
			for k := range allDayKeys {
				d, _ := time.ParseInLocation("2006-01-02", k, tz)
				if earliest.IsZero() || d.Before(earliest) {
					earliest = d
				}
			}
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz)
			// max(_, 1) clamps the rare future-dated-record case so
			// "X/Y" denominators never go below 1.
			agg.RangeDays = max(int(today.Sub(earliest).Hours()/24)+1, 1)
		}
	}

	agg.LongestStreak, agg.CurrentStreak = computeStreaks(allDayKeys, now, tz)
	agg.FavoriteModel = pickFavorite(modelTokens)

	return agg, nil
}

func computeStreaks(activeDays map[string]struct{}, now time.Time, tz *time.Location) (longest, current int) {
	if len(activeDays) == 0 {
		return 0, 0
	}
	keys := make([]string, 0, len(activeDays))
	for k := range activeDays {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	prev := time.Time{}
	run := 0
	for _, k := range keys {
		// All keys are produced by Format("2006-01-02") earlier in
		// this file, so ParseInLocation cannot fail; ignore err.
		d, _ := time.ParseInLocation("2006-01-02", k, tz)
		if !prev.IsZero() && d.Sub(prev) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		prev = d
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz)
	if _, ok := activeDays[today.Format("2006-01-02")]; !ok {
		return longest, 0
	}
	current = 0
	for d := today; ; d = d.AddDate(0, 0, -1) {
		if _, ok := activeDays[d.Format("2006-01-02")]; !ok {
			break
		}
		current++
	}
	return longest, current
}

func pickFavorite(modelTokens map[string]int64) string {
	if len(modelTokens) == 0 {
		return ""
	}
	type entry struct {
		stripped string
		tokens   int64
	}
	entries := make([]entry, 0, len(modelTokens))
	for k, v := range modelTokens {
		entries = append(entries, entry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].tokens != entries[j].tokens {
			return entries[i].tokens > entries[j].tokens
		}
		return entries[i].stripped < entries[j].stripped
	})
	return ModelDisplay(entries[0].stripped)
}
