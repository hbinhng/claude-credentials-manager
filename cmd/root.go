package cmd

import (
	"fmt"
	"os"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ccm",
	Short: "Claude Credentials Manager — manage multiple Claude OAuth sessions",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// requireOnline is the PreRunE hook used by every ccm command that
// issues network I/O. It validates and caches CCM_PROXY via
// internal/httpx so a malformed value fails the command before any
// upstream request, without touching offline commands like
// `ccm version` or `ccm restore` which should work regardless of
// CCM_PROXY state.
func requireOnline(cmd *cobra.Command, args []string) error {
	return httpx.Configure()
}
