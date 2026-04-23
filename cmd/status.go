package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().Bool("no-quota", false, "skip the live quota API call (faster, offline-safe)")
	statusCmd.Flags().StringP("output", "o", "table", "output format: table or json")
	statusCmd.PreRunE = requireOnline
}

// StatusReport is the envelope emitted by `ccm status -o json`. It is a
// stable public schema — field names and types are a contract with
// anyone piping `jq` at our output, so do NOT rename without bumping
// Version and announcing the break. See docs/ccm.1 for details.
type StatusReport struct {
	Version     int           `json:"version"`
	ActiveID    string        `json:"activeId,omitempty"`
	Credentials []StatusEntry `json:"credentials"`
}

// StatusEntry describes a single credential in the report. Timestamps
// are RFC3339 UTC; Tier is *string so a missing tier marshals to JSON
// null (not the human-readable "-" placeholder used in the table).
type StatusEntry struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Tier            *string     `json:"tier"`
	Status          string      `json:"status"` // "valid" | "expiring_soon" | "expired"
	Active          bool        `json:"active"`
	ExpiresAt       string      `json:"expiresAt"`
	CreatedAt       string      `json:"createdAt"`
	LastRefreshedAt string      `json:"lastRefreshedAt"`
	Quota           QuotaStatus `json:"quota"`
}

// QuotaStatus carries per-credential quota state in three distinct
// shapes: not fetched, fetched with windows, or fetched with an error.
// Consumers should branch on Fetched first.
type QuotaStatus struct {
	Fetched bool          `json:"fetched"`
	Error   string        `json:"error,omitempty"`
	Windows []QuotaWindow `json:"windows,omitempty"`
}

type QuotaWindow struct {
	Name     string  `json:"name"`
	Used     float64 `json:"used"`
	ResetsAt string  `json:"resetsAt"` // raw RFC3339 upstream timestamp
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "List all credentials with status and usage",
	RunE: func(cmd *cobra.Command, args []string) error {
		noQuota, _ := cmd.Flags().GetBool("no-quota")
		output, _ := cmd.Flags().GetString("output")

		switch output {
		case "table", "json":
		default:
			return fmt.Errorf("invalid --output %q: must be 'table' or 'json'", output)
		}

		creds, err := store.List()
		if err != nil {
			return err
		}
		sort.Slice(creds, func(i, j int) bool {
			return creds[i].Name < creds[j].Name
		})

		// Human table mode prints a friendly "no credentials" line;
		// JSON mode always emits a valid empty envelope so scripts can
		// consume it unconditionally.
		if len(creds) == 0 && output == "table" {
			fmt.Println("No credentials found. Use `ccm login` to add one.")
			return nil
		}

		activeID := claude.ActiveID()

		var usages []*oauth.UsageInfo
		if !noQuota {
			usages = fetchUsagesParallel(creds)
		} else {
			usages = make([]*oauth.UsageInfo, len(creds))
		}

		report := buildStatusReport(creds, usages, activeID, noQuota)

		if output == "json" {
			return writeStatusJSON(os.Stdout, report)
		}
		return renderStatusTable(report)
	},
}

// fetchUsagesParallel fetches quota usage for each non-expired
// credential concurrently via oauth.FetchUsageFn. Returns a slice
// aligned by index with creds; entries for expired credentials
// are left nil. The seam lets tests and the upcoming ccm serve
// web handler inject a fake FetchUsageFn without HTTP round-trips.
func fetchUsagesParallel(creds []*store.Credential) []*oauth.UsageInfo {
	usages := make([]*oauth.UsageInfo, len(creds))
	var wg sync.WaitGroup
	for i, c := range creds {
		if c.IsExpired() {
			continue
		}
		wg.Add(1)
		go func(i int, token string) {
			defer wg.Done()
			usages[i] = oauth.FetchUsageFn(token)
		}(i, c.ClaudeAiOauth.AccessToken)
	}
	wg.Wait()
	return usages
}

// writeStatusJSON emits the StatusReport as minified JSON with a single
// trailing newline. Consumers who want human-friendly indentation can
// pipe through `jq` — keeping the default compact saves bytes on the
// wire when status is piped to files or other tools.
func writeStatusJSON(w io.Writer, r StatusReport) error {
	return json.NewEncoder(w).Encode(r)
}

// buildStatusReport converts raw store data + parallel usage results
// into a stable, serialization-ready StatusReport. It is pure — no I/O,
// no clock calls beyond what store.Credential exposes — so it can be
// tested directly without spinning up cobra.
func buildStatusReport(creds []*store.Credential, usages []*oauth.UsageInfo, activeID string, noQuota bool) StatusReport {
	entries := make([]StatusEntry, 0, len(creds))
	for i, c := range creds {
		var tier *string
		if c.Subscription.Tier != "" {
			t := c.Subscription.Tier
			tier = &t
		}

		entry := StatusEntry{
			ID:              c.ID,
			Name:            c.Name,
			Tier:            tier,
			Status:          strings.ReplaceAll(c.Status(), " ", "_"),
			Active:          activeID != "" && c.ID == activeID,
			ExpiresAt:       time.UnixMilli(c.ClaudeAiOauth.ExpiresAt).UTC().Format(time.RFC3339),
			CreatedAt:       c.CreatedAt,
			LastRefreshedAt: c.LastRefreshedAt,
		}

		// Quota state: --no-quota always wins (scripts opted out). An
		// expired credential is never fetched either (the caller leaves
		// usages[i] == nil). Otherwise mirror the fetch outcome.
		if noQuota || i >= len(usages) || usages[i] == nil {
			entry.Quota = QuotaStatus{Fetched: false}
		} else {
			entry.Quota = QuotaStatus{Fetched: true}
			if usages[i].Error != "" {
				entry.Quota.Error = usages[i].Error
			} else {
				entry.Quota.Windows = make([]QuotaWindow, 0, len(usages[i].Quotas))
				for _, q := range usages[i].Quotas {
					entry.Quota.Windows = append(entry.Quota.Windows, QuotaWindow{
						Name:     q.Name,
						Used:     q.Used,
						ResetsAt: q.ResetsAt,
					})
				}
			}
		}
		entries = append(entries, entry)
	}

	return StatusReport{
		Version:     1,
		ActiveID:    activeID,
		Credentials: entries,
	}
}

// renderStatusTable prints the existing human-friendly table format,
// reading from a StatusReport so both output modes share one code path
// above it. Presentation-only transforms (8-char ID prefix, "-" for
// missing tier, relative reset strings, spaced "expiring soon") happen
// here, not in the JSON schema.
func renderStatusTable(report StatusReport) error {
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tTIER\tSTATUS\tEXPIRES\tACTIVE")
	for _, e := range report.Credentials {
		active := ""
		if e.Active {
			active = "*"
		}
		displayName := e.Name
		if displayName == e.ID {
			displayName = e.ID[:8] + "..."
		}
		tier := "-"
		if e.Tier != nil {
			tier = *e.Tier
		}
		status := strings.ReplaceAll(e.Status, "_", " ")

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID[:8],
			displayName,
			tier,
			status,
			relativeExpires(e.ExpiresAt),
			active,
		)

		if !e.Quota.Fetched {
			continue
		}
		if e.Quota.Error != "" {
			fmt.Fprintf(w, "\t\t\tquota: error\t\t\n")
			continue
		}
		for _, q := range e.Quota.Windows {
			reset := ""
			if q.ResetsAt != "" {
				reset = " (resets " + oauth.FormatResetTime(q.ResetsAt) + ")"
			}
			// Table shows percentage remaining; clamp negatives for the
			// rare upstream "over-utilization" case (e.g. used > 100).
			remaining := 100 - q.Used
			if remaining < 0 {
				remaining = 0
			}
			fmt.Fprintf(w, "\t\t\t%s: %.0f%%%s\t\t\n", q.Name, remaining, reset)
		}
	}
	return w.Flush()
}

// relativeExpires renders an RFC3339 ExpiresAt as a short relative
// string ("in 2 hrs", "15 mins ago") for the table. It mirrors the old
// Credential.ExpiresIn method but takes the already-formatted timestamp
// from the report so the table renderer never peeks back into store.
func relativeExpires(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	diff := time.Until(t)
	if diff <= 0 {
		ago := -diff
		switch {
		case ago < time.Minute:
			return "just now"
		case ago < time.Hour:
			return fmt.Sprintf("%d mins ago", int(ago.Minutes()))
		default:
			return fmt.Sprintf("%d hrs ago", int(ago.Hours()))
		}
	}
	switch {
	case diff < time.Minute:
		return fmt.Sprintf("in %d secs", int(diff.Seconds()))
	case diff < time.Hour:
		return fmt.Sprintf("in %d mins", int(diff.Minutes()))
	default:
		return fmt.Sprintf("in %d hrs", int(diff.Hours()))
	}
}
