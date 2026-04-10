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

// versionString returns the multi-line "<semver>\ncommit: ...\nbuilt: ..."
// payload that goes into rootCmd.Version. The leading "ccm version " prefix
// is added by Cobra's default version template — see init() for why we do
// not call SetVersionTemplate.
func versionString() string {
	return fmt.Sprintf("%s\ncommit: %s\nbuilt:  %s", Version, Commit, BuildDate)
}

// versionCmd mirrors `ccm --version` as an explicit subcommand. Cobra
// auto-wires the --version flag when rootCmd.Version is set, but does not
// create a `version` subcommand — we add one so both forms work and produce
// byte-identical output.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print ccm version, commit, and build date",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("ccm version " + versionString())
	},
}

func init() {
	rootCmd.Version = versionString()
	// NOTE: do NOT call rootCmd.SetVersionTemplate. Doing so flips Cobra
	// onto a code path that pulls in ~2 MB of text/template machinery the
	// linker would otherwise dead-code-eliminate. v1.6.3 shipped with that
	// regression. Cobra's default version template ("{{.Name}} version
	// {{.Version}}") already renders our multi-line Version field correctly.
	rootCmd.AddCommand(versionCmd)
}
