package cmd

import (
	"fmt"
	"os"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
	"github.com/spf13/cobra"
)

// claudeSyncFn is the seam tests override to avoid touching ~/.claude/.
var claudeSyncFn = claude.Sync

var rootCmd = &cobra.Command{
	Use:               "ccm",
	Short:             "Claude Credentials Manager — manage multiple Claude OAuth sessions",
	PersistentPreRunE: rootPersistentPreRunE,
}

// syncSkipFor reports whether a command should bypass the auto-sync hook.
// Exempt commands are read-only or shell plumbing where the extra I/O
// would be wasted: shell tab-completion latency, or shell-init bootstrap
// like `source <(ccm completion bash)` which would otherwise run sync
// during every new terminal.
//
// Walks parents so `ccm completion bash` (cmd.Name() == "bash") still
// matches the "completion" entry via its parent.
func syncSkipFor(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "completion", "version", "help", "__complete", "__completeNoDesc":
			return true
		}
	}
	return false
}

func rootPersistentPreRunE(cmd *cobra.Command, _ []string) error {
	if syncSkipFor(cmd) {
		return nil
	}
	if _, err := claudeSyncFn(); err != nil {
		fmt.Fprintf(os.Stderr, "ccm: sync skipped: %v\n", err)
	}
	return nil
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
