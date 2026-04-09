package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
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
		fmt.Fprintln(w, "ID\tNAME\tTIER\tSTATUS\tEXPIRES\tACTIVE")
		for _, c := range creds {
			active := ""
			if c.ID == activeID {
				active = "*"
			}
			displayName := c.Name
			if displayName == c.ID {
				displayName = c.ID[:8] + "..."
			}
			tier := c.Subscription.Tier
			if tier == "" {
				tier = "-"
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				c.ID[:8],
				displayName,
				tier,
				c.Status(),
				c.ExpiresIn(),
				active,
			)

			if c.IsExpired() {
				continue
			}
			info := oauth.FetchUsage(c.ClaudeAiOauth.AccessToken)
			if info.Error != "" {
				fmt.Fprintf(w, "\t\t\tquota: error\t\t\n")
			} else {
				for _, q := range info.Quotas {
					reset := ""
					if q.ResetsAt != "" {
						reset = " (resets " + q.ResetsAt + ")"
					}
					fmt.Fprintf(w, "\t\t\t%s: %.0f%%%s\t\t\n", q.Name, q.Remaining, reset)
				}
			}
		}
		w.Flush()
		return nil
	},
}
