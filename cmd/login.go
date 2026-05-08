package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.AddCommand(loginClaudeCmd)
	loginCmd.AddCommand(loginCodexCmd)
	// requireOnline applies to both login flows — they hit
	// auth.anthropic.com and auth.openai.com respectively.
	loginClaudeCmd.PreRunE = requireOnline
	loginCodexCmd.PreRunE = requireOnline
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Capture a new OAuth credential",
	Long: `Capture a new OAuth credential. Specify a provider:

  ccm login claude   for Anthropic OAuth (Claude Code)
  ccm login codex    for OpenAI/ChatGPT OAuth (codex CLI)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = cmd.Help()
		return errors.New("specify a provider: ccm login claude | ccm login codex")
	},
}
