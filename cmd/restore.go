package cmd

import (
	"github.com/hbinhng/ccm/internal/claude"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(restoreCmd)
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore original Claude credentials (undo `ccm use`)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return claude.Restore()
	},
}
