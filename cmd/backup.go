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

// backupProfile is the local view of oauth.Profile so tests can stub
// the fetcher without importing the oauth package surface.
type backupProfile struct {
	Email string
	Tier  string
}

// Seam: production uses the real network call.
var backupFetchProfileFn = func(token string) backupProfile {
	p := oauth.FetchProfile(token)
	return backupProfile{Email: p.Email, Tier: p.Tier}
}

// Seam: production uses claude.Sync (explicit error propagation).
var backupSyncFn = claude.Sync

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.PreRunE = requireOnline
}

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Import the active Claude credentials into the ccm store",
	Long: `Import the credential currently active in Claude Code (file or
keychain) into the ccm store. If the active blob already carries a
ccmSourceId marker pointing at an existing store credential, the store
entry is updated with the tokens Claude currently has. Otherwise its
OAuth tokens are copied into ~/.ccm/, decorated with a fresh UUID and
profile metadata (email as the name, subscription tier), and saved as
a normal ccm-managed credential.

This does not activate the imported credential; use ` + "`ccm use`" + ` for that.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBackup()
	},
}

func runBackup() error {
	blob, ok, err := claude.ReadActiveBlob()
	if err != nil {
		return fmt.Errorf("read active credentials: %w", err)
	}
	if !ok {
		return fmt.Errorf("no Claude credentials present")
	}

	if id, present := claude.Active(); present {
		if cred, err := store.Load(id); err == nil {
			fmt.Printf("Syncing credentials for %s (%s)...\n", cred.Name, id[:min(8, len(id))])
			changed, syncErr := backupSyncFn()
			if syncErr != nil {
				return fmt.Errorf("sync: %w", syncErr)
			}
			if changed {
				fmt.Println("Synced.")
			} else {
				fmt.Println("Already up to date.")
			}
			return nil
		}
		fmt.Fprintf(os.Stderr, "ccm: active credential %s missing from store; importing as new\n", id[:min(8, len(id))])
	}

	var parsed struct {
		ClaudeAiOauth store.OAuthTokens `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(blob, &parsed); err != nil {
		return fmt.Errorf("parse credentials: %w", err)
	}
	if parsed.ClaudeAiOauth.AccessToken == "" {
		return fmt.Errorf("no claudeAiOauth.accessToken found in active Claude credentials")
	}

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	profile := backupFetchProfileFn(parsed.ClaudeAiOauth.AccessToken)
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
}
