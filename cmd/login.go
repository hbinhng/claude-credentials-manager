package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.PreRunE = requireOnline
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate a new Claude account via OAuth",
	RunE: func(cmd *cobra.Command, args []string) error {
		reader := bufio.NewReader(os.Stdin)
		tokens, err := oauth.Login(func() (string, error) {
			return reader.ReadString('\n')
		})
		if err != nil {
			return err
		}

		id := uuid.New().String()
		now := time.Now().UTC().Format(time.RFC3339)

		scopes := strings.Fields(tokens.Scope)
		if len(scopes) == 0 {
			scopes = oauth.Scopes
		}

		profile := oauth.FetchProfile(tokens.AccessToken)
		name := profile.Email
		if name == "" {
			name = id
		}

		cred := &store.Credential{
			ID:   id,
			Name: name,
			ClaudeAiOauth: store.OAuthTokens{
				AccessToken:  tokens.AccessToken,
				RefreshToken: tokens.RefreshToken,
				ExpiresAt:    time.Now().UnixMilli() + tokens.ExpiresIn*1000,
				Scopes:       scopes,
			},
			Subscription:    store.Subscription{Tier: profile.Tier},
			CreatedAt:       now,
			LastRefreshedAt: now,
		}

		if err := store.Save(cred); err != nil {
			return fmt.Errorf("save credentials: %w", err)
		}

		fmt.Printf("\nLogged in as %s\n", name)
		if name == id {
			fmt.Printf("Use `ccm rename %s <name>` to set a friendly name.\n", id[:8])
		}
		return nil
	},
}
