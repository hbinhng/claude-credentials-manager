package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(launchCmd)
	launchCmd.Flags().String("via", "", "ticket emitted by `ccm share` on the host side")
	_ = launchCmd.MarkFlagRequired("via")
}

var launchCmd = &cobra.Command{
	Use:   "launch --via <ticket> [-- <claude args>]",
	Short: "Launch Claude Code against a remote ccm share session",
	Long: `Decode a share ticket and launch Claude Code pointed at the remote
proxy behind it. The ticket carries the tunnel host and the access
token, both of which are forwarded to Claude Code via environment
variables:

  ANTHROPIC_BASE_URL=https://<tunnel-host>
  ANTHROPIC_AUTH_TOKEN=<access-token>

Any arguments after ` + "`--`" + ` are passed to ` + "`claude`" + ` unchanged, so you can
use ` + "`ccm launch --via <ticket> -- -p 'hi'`" + ` for a one-shot query.

This command does not require a ccm-managed credential on the local
machine; the bearer comes from the ticket. You only need ` + "`claude`" + ` on
PATH.

If running any binary is not allowed on the remote side, decode the
ticket manually:

  echo <TICKET> | base64 -d
  # -> https://<token>@<host>

and set the two env vars yourself before invoking ` + "`claude`" + `.`,
	// Everything after the first positional goes to claude verbatim.
	DisableFlagsInUseLine: true,
	FParseErrWhitelist: cobra.FParseErrWhitelist{
		UnknownFlags: false,
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, _ := cmd.Flags().GetString("via")
		ticket, err := share.DecodeTicket(raw)
		if err != nil {
			return fmt.Errorf("invalid ticket: %w", err)
		}

		claudeBin, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("could not find 'claude' on PATH")
		}

		child := exec.Command(claudeBin, args...)
		child.Env = append(os.Environ(),
			"ANTHROPIC_BASE_URL=https://"+ticket.Host,
			"ANTHROPIC_AUTH_TOKEN="+ticket.Token,
		)
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr

		if err := child.Run(); err != nil {
			// Propagate the child's exit code so shell pipelines behave.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				os.Exit(exitErr.ExitCode())
			}
			return err
		}
		return nil
	},
}
