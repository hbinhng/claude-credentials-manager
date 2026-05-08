package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/codex"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(restoreCmd)
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore original credentials (undo `ccm use`)",
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		any := false

		// claude.Restore() returns nil even when no backup exists (it just
		// clears the managed entry). Pre-check the backup file so we only
		// report success when a backup was actually restored.
		home, _ := os.UserHomeDir()
		claudeBackup := filepath.Join(home, ".claude", "bk.credentials.json")
		if _, err := os.Stat(claudeBackup); err == nil {
			if err := claude.Restore(); err == nil {
				fmt.Fprintln(out, "Restored ~/.claude/.credentials.json from bk.credentials.json")
				any = true
			}
		}

		// codex.Restore() returns a non-nil error when no backup exists,
		// so a nil return is a reliable success signal.
		if err := codex.Restore(); err == nil {
			fmt.Fprintln(out, "Restored ~/.codex/auth.json from bk.auth.json")
			any = true
		}

		if !any {
			fmt.Fprintln(out, "no backup found for either provider; nothing to restore")
		}
		return nil
	},
}
