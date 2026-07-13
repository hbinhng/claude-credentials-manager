package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.AddCommand(loginClaudeCmd)
	loginCmd.AddCommand(loginCodexCmd)
	loginCmd.AddCommand(loginGrokCmd)
	// requireOnline applies to all three login flows — they hit
	// auth.anthropic.com, auth.openai.com, and auth.x.ai respectively.
	loginClaudeCmd.PreRunE = requireOnline
	loginCodexCmd.PreRunE = requireOnline
	loginGrokCmd.PreRunE = requireOnline
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Capture a new OAuth credential",
	Long: `Capture a new OAuth credential. Specify a provider:

  ccm login claude   for Anthropic OAuth (Claude Code)
  ccm login codex    for OpenAI/ChatGPT OAuth (codex CLI)
  ccm login grok     for xAI/Grok OAuth (SuperGrok / X Premium+)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = cmd.Help()
		return errors.New("specify a provider: ccm login claude | ccm login codex | ccm login grok")
	},
}
