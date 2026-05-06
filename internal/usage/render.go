package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// Render writes a full stats report (heatmap + totals) to w.
func Render(w io.Writer, agg *Aggregate, r Range, now time.Time, tz *time.Location) {
	if agg == nil || (agg.Total == 0 && len(agg.DailyTokens) == 0) {
		fmt.Fprintln(w, "No usage recorded yet.")
		return
	}
	renderHeatmap(w, agg, now, tz)
	fmt.Fprintln(w)
	renderTotals(w, agg, rangeLabel(r))
}

// RenderJSON dumps the Aggregate as indented JSON.
func RenderJSON(w io.Writer, agg *Aggregate) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(agg)
}

// rangeLabel renders the human-readable window name.
func rangeLabel(r Range) string {
	switch r {
	case Range7d:
		return "Last 7 days"
	case Range30d:
		return "Last 30 days"
	default:
		return "All time"
	}
}

// formatTokens renders a token count with k / m / b suffix.
func formatTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1_000_000)
	default:
		return fmt.Sprintf("%.1fb", float64(n)/1_000_000_000)
	}
}

// formatDuration renders a duration as "Xd Yh Zm" / "Xh Ym" / "Xm".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "0m"
	}
	days := int(d / (24 * time.Hour))
	rem := d - time.Duration(days)*24*time.Hour
	hours := int(rem / time.Hour)
	rem -= time.Duration(hours) * time.Hour
	mins := int(rem / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// renderHeatmap writes the 52-week × 7-day grid.
//
// Row 0 = Sunday (top); row 6 = Saturday (bottom). Right column =
// the week containing `today`. Days after `today` render as space.
func renderHeatmap(w io.Writer, agg *Aggregate, now time.Time, tz *time.Location) {
	const weeks = 52
	const days = 7

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz)
	daysSinceSunday := int(today.Weekday())
	rightColEnd := today.AddDate(0, 0, 6-daysSinceSunday) // the Saturday of "today"'s week
	gridStart := rightColEnd.AddDate(0, 0, -(weeks*7 - 1))

	// Quartile cuts on non-zero days within the heatmap window.
	var nonZero []int64
	for d := gridStart; !d.After(rightColEnd); d = d.AddDate(0, 0, 1) {
		if v := agg.DailyTokens[d.Format("2006-01-02")]; v > 0 {
			nonZero = append(nonZero, v)
		}
	}
	sort.Slice(nonZero, func(i, j int) bool { return nonZero[i] < nonZero[j] })
	q1, q2, q3 := quartiles(nonZero)

	glyph := func(v int64) string {
		switch {
		case v == 0:
			return "·"
		case v <= q1:
			return "░"
		case v <= q2:
			return "▒"
		case v <= q3:
			return "▓"
		default:
			return "█"
		}
	}

	fmt.Fprintln(w, "      "+buildMonthHeader(gridStart, weeks))

	dayLabels := []string{"   ", "Mon", "   ", "Wed", "   ", "Fri", "   "}
	for row := 0; row < days; row++ {
		fmt.Fprintf(w, "  %s ", dayLabels[row])
		for col := 0; col < weeks; col++ {
			d := gridStart.AddDate(0, 0, col*7+row)
			if d.After(today) {
				fmt.Fprint(w, " ")
				continue
			}
			fmt.Fprint(w, glyph(agg.DailyTokens[d.Format("2006-01-02")]))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "      Less ░ ▒ ▓ █ More")
}

func quartiles(sorted []int64) (q1, q2, q3 int64) {
	n := len(sorted)
	if n == 0 {
		return 0, 0, 0
	}
	q1 = sorted[n/4]
	q2 = sorted[n/2]
	q3 = sorted[3*n/4]
	return
}

func buildMonthHeader(start time.Time, weeks int) string {
	out := make([]rune, weeks)
	for i := range out {
		out[i] = ' '
	}
	prevMonth := start.Month()
	for col := 0; col < weeks; col++ {
		d := start.AddDate(0, 0, col*7)
		if col == 0 || d.Month() != prevMonth {
			label := d.Format("Jan")
			for j, r := range label {
				if col+j < weeks {
					out[col+j] = r
				}
			}
			prevMonth = d.Month()
		}
	}
	return string(out)
}

// renderTotals writes the metrics block under the heatmap.
func renderTotals(w io.Writer, agg *Aggregate, rangeName string) {
	fmt.Fprintln(w, "      "+rangeName)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "      Favorite model: %s\n", agg.FavoriteModel)
	fmt.Fprintf(w, "      Total tokens: %s\n", formatTokens(agg.Total))
	fmt.Fprintf(w, "      Read: %s   Cached read: %s\n",
		formatTokens(agg.Read), formatTokens(agg.CachedRead))
	fmt.Fprintf(w, "      Write: %s   Cached write: %s\n",
		formatTokens(agg.Write), formatTokens(agg.CachedWrite))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "      Sessions: %d\t\tLongest session: %s\n",
		agg.Sessions, formatDuration(agg.LongestSession))
	fmt.Fprintf(w, "      Active days: %d/%d\tLongest streak: %d days\n",
		agg.ActiveDays, agg.RangeDays, agg.LongestStreak)
	fmt.Fprintf(w, "      Most active day: %s\tCurrent streak: %d days\n",
		formatMonthDay(agg.MostActiveDay), agg.CurrentStreak)
}

func formatMonthDay(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("Jan 2")
}
