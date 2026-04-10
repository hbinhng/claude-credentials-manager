package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version, Commit, and BuildDate are populated at link time by the Makefile
// via -ldflags "-X github.com/hbinhng/claude-credentials-manager/cmd.<name>=...".
// VERSION comes from npm/package.json (the canonical version source),
// COMMIT from `git rev-parse --short HEAD`, and BUILD_DATE from `date -u`
// in ISO-8601. A plain `go build .` (bypassing the Makefile) leaves these
// at their "dev"/"unknown" placeholders, which is a clear signal that the
// binary is an untagged local build.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// versionString is what Cobra prints for `ccm --version` and `ccm version`.
// Rendered as three short lines so long commit SHAs and RFC3339 timestamps
// don't wrap awkwardly in typical terminals.
func versionString() string {
	return fmt.Sprintf("ccm %s\ncommit: %s\nbuilt:  %s", Version, Commit, BuildDate)
}

// versionCmd mirrors `ccm --version` as an explicit subcommand. Cobra
// auto-wires the --version flag when rootCmd.Version is set, but does not
// create a `version` subcommand — we add one so both forms work.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print ccm version, commit, and build date",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(versionString())
	},
}

func init() {
	rootCmd.Version = versionString()
	// Strip Cobra's "{Name} version {Version}" wrapper — versionString()
	// already starts with "ccm <version>".
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
}
