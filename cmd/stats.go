package cmd

import (
	"fmt"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/usage"
	"github.com/spf13/cobra"
)

var (
	statsRange   string
	statsJSON    bool
	statsSession string
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show aggregated usage stats from ccm share / ccm launch",
	Long: `Reads ~/.ccm/usage/*.ndjson (one file per Claude Code session) and prints
a heatmap plus token totals broken down by Read / Cached read / Write /
Cached write. The heatmap always shows the trailing 52 weeks; only the
totals/sessions block honors --range.

Streaks always span the full unfiltered history regardless of --range.`,
	RunE: runStats,
}

func init() {
	statsCmd.Flags().StringVar(&statsRange, "range", "all", "time window for metrics: all | 7d | 30d")
	statsCmd.Flags().BoolVar(&statsJSON, "json", false, "dump aggregate as JSON instead of rendered output")
	statsCmd.Flags().StringVar(&statsSession, "session", "", "filter to a single Claude Code session ID (UUID)")
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, _ []string) error {
	r, err := parseRange(statsRange)
	if err != nil {
		return err
	}
	tz := time.Local
	f := usage.Filter{Range: r, SessionID: statsSession}
	agg, err := usage.LoadAggregate(f, tz)
	if err != nil {
		return fmt.Errorf("load aggregate: %w", err)
	}
	if statsJSON {
		return usage.RenderJSON(cmd.OutOrStdout(), agg)
	}
	usage.Render(cmd.OutOrStdout(), agg, r, time.Now().In(tz), tz)
	return nil
}

func parseRange(s string) (usage.Range, error) {
	switch s {
	case "all":
		return usage.RangeAll, nil
	case "7d":
		return usage.Range7d, nil
	case "30d":
		return usage.Range30d, nil
	default:
		return 0, fmt.Errorf("invalid --range %q (expected: all | 7d | 30d)", s)
	}
}
