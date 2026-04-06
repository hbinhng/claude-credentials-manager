package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/hbinhng/ccm/internal/claude"
	"github.com/hbinhng/ccm/internal/oauth"
	"github.com/hbinhng/ccm/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "List all credentials with status and usage",
	RunE: func(cmd *cobra.Command, args []string) error {
		creds, err := store.List()
		if err != nil {
			return err
		}
		if len(creds) == 0 {
			fmt.Println("No credentials found. Use `ccm login` to add one.")
			return nil
		}

		sort.Slice(creds, func(i, j int) bool {
			return creds[i].Name < creds[j].Name
		})

		activeID := claude.ActiveID()

		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tEXPIRES\tUSAGE\tACTIVE")
		for _, c := range creds {
			active := ""
			if c.ID == activeID {
				active = "*"
			}
			displayName := c.Name
			if displayName == c.ID {
				displayName = c.ID[:8] + "..."
			}

			usage := fetchUsageSummary(c)

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				c.ID[:8],
				displayName,
				c.Status(),
				c.ExpiresIn(),
				usage,
				active,
			)
		}
		w.Flush()
		return nil
	},
}

func fetchUsageSummary(c *store.Credential) string {
	if c.IsExpired() {
		return "-"
	}

	info := oauth.FetchUsage(c.ClaudeAiOauth.AccessToken)
	if info.Error != "" {
		return "err"
	}
	if len(info.Quotas) == 0 {
		return "ok"
	}

	var parts []string
	for _, q := range info.Quotas {
		parts = append(parts, fmt.Sprintf("%s:%.0f%%", q.Name, q.Remaining))
	}
	return strings.Join(parts, " ")
}
