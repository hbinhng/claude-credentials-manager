package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(useCmd)
}

var useCmd = &cobra.Command{
	Use:               "use <id-or-name>",
	Short:             "Activate a credential for Claude Code",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeCredential,
	RunE: func(cmd *cobra.Command, args []string) error {
		cred, err := store.Resolve(args[0])
		if err != nil {
			return err
		}

		if cred.IsExpired() {
			fmt.Printf("Token for %s (%s) is expired.\n", cred.Name, cred.ID[:8])
			fmt.Print("Refresh it now? [Y/n] ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer == "" || answer == "y" || answer == "yes" {
				tokens, err := oauth.Refresh(cred.ClaudeAiOauth.RefreshToken)
				if err != nil {
					return fmt.Errorf("refresh failed: %w\nRe-authenticate with `ccm login`", err)
				}
				scopes := strings.Fields(tokens.Scope)
				if len(scopes) == 0 {
					scopes = cred.ClaudeAiOauth.Scopes
				}
				cred.ClaudeAiOauth.AccessToken = tokens.AccessToken
				if tokens.RefreshToken != "" {
					cred.ClaudeAiOauth.RefreshToken = tokens.RefreshToken
				}
				cred.ClaudeAiOauth.ExpiresAt = time.Now().UnixMilli() + tokens.ExpiresIn*1000
				cred.ClaudeAiOauth.Scopes = scopes
				if err := store.Save(cred); err != nil {
					return fmt.Errorf("save refreshed credential: %w", err)
				}
				fmt.Println("Token refreshed.")
			} else {
				return fmt.Errorf("cannot activate expired credential")
			}
		}

		if err := claude.Use(cred); err != nil {
			return err
		}

		displayName := cred.Name
		if displayName == cred.ID {
			displayName = cred.ID[:8] + "..."
		}
		fmt.Printf("Now using '%s' (%s)\n", displayName, cred.ID[:8])
		return nil
	},
}
