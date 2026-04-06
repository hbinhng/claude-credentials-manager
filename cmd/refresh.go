package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/hbinhng/ccm/internal/claude"
	"github.com/hbinhng/ccm/internal/oauth"
	"github.com/hbinhng/ccm/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(refreshCmd)
}

var refreshCmd = &cobra.Command{
	Use:   "refresh <id-or-name>",
	Short: "Refresh OAuth token for a credential",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cred, err := store.Resolve(args[0])
		if err != nil {
			return err
		}

		oldExpiry := time.UnixMilli(cred.ClaudeAiOauth.ExpiresAt)
		fmt.Printf("Refreshing %s (%s)...\n", cred.Name, cred.ID[:8])

		tokens, err := oauth.Refresh(cred.ClaudeAiOauth.RefreshToken)
		if err != nil {
			if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
				return fmt.Errorf("refresh token expired or revoked. Re-authenticate with `ccm login`")
			}
			return err
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
		cred.LastRefreshedAt = time.Now().UTC().Format(time.RFC3339)

		if err := store.Save(cred); err != nil {
			return fmt.Errorf("save credentials: %w", err)
		}

		newExpiry := time.UnixMilli(cred.ClaudeAiOauth.ExpiresAt)
		fmt.Printf("Refreshed. Old expiry: %s → New expiry: %s\n",
			oldExpiry.Local().Format("15:04:05"),
			newExpiry.Local().Format("15:04:05"),
		)

		// If this credential is the active one, update ~/.claude/ccm.credentials.json
		if claude.IsActive(cred.ID) {
			if err := claude.WriteActive(cred); err != nil {
				fmt.Printf("Warning: could not update active credential: %v\n", err)
			} else {
				fmt.Println("Active credential updated.")
			}
		}

		return nil
	},
}
