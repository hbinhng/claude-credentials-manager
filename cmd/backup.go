package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.PreRunE = requireOnline
}

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Import the current ~/.claude/.credentials.json into the ccm store",
	Long: `Import the credential currently in ~/.claude/.credentials.json into the
ccm store. If the file is already managed by ccm, nothing is done. Otherwise
its OAuth tokens are copied into ~/.ccm/, decorated with a fresh UUID and
profile metadata (email as the name, subscription tier), and saved as a
normal ccm-managed credential.

This does not activate the imported credential; use ` + "`ccm use`" + ` for that.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := claude.CredentialsPath()

		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("~/.claude/.credentials.json does not exist")
		}

		if claude.IsManaged() {
			fmt.Println("~/.claude/.credentials.json is already managed by ccm. Nothing to do.")
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read credentials: %w", err)
		}

		var parsed struct {
			ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return fmt.Errorf("parse credentials: %w", err)
		}
		if parsed.ClaudeAiOauth.AccessToken == "" {
			return fmt.Errorf("no claudeAiOauth.accessToken found in ~/.claude/.credentials.json")
		}

		id := uuid.New().String()
		now := time.Now().UTC().Format(time.RFC3339)

		profile := oauth.FetchProfile(parsed.ClaudeAiOauth.AccessToken)
		name := profile.Email
		if name == "" {
			name = id
		}

		cred := &store.Credential{
			ID:              id,
			Name:            name,
			ClaudeAiOauth:   parsed.ClaudeAiOauth,
			Subscription:    store.Subscription{Tier: profile.Tier},
			CreatedAt:       now,
			LastRefreshedAt: now,
		}

		if err := store.Save(cred); err != nil {
			return fmt.Errorf("save credentials: %w", err)
		}

		fmt.Printf("Imported credential as %s (%s)\n", name, id[:8])
		if name == id {
			fmt.Printf("Use `ccm rename %s <name>` to set a friendly name.\n", id[:8])
		}
		fmt.Println("Run `ccm use` to activate it.")
		return nil
	},
}
