package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

var refreshAll bool

func init() {
	refreshCmd.Flags().BoolVarP(&refreshAll, "all", "a", false, "Refresh all credentials")
	rootCmd.AddCommand(refreshCmd)
}

var refreshCmd = &cobra.Command{
	Use:               "refresh [id-or-name]",
	Short:             "Refresh OAuth token for a credential",
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeCredential,
	RunE: func(cmd *cobra.Command, args []string) error {
		if refreshAll {
			return refreshAllCredentials()
		}

		if len(args) == 0 {
			return fmt.Errorf("specify a credential id or name, or use --all to refresh all")
		}

		return refreshCredential(args[0])
	},
}

func refreshCredential(identity string) error {
	cred, err := doRefreshCredential(identity, fmt.Printf)
	if err != nil {
		return err
	}

	if claude.IsActive(cred.ID) {
		if err := claude.WriteActive(cred); err != nil {
			fmt.Printf("Warning: could not update active credential: %v\n", err)
		} else {
			fmt.Println("Active credential updated.")
		}
	}

	return nil
}

func doRefreshCredential(identity string, printf func(string, ...any) (int, error)) (*store.Credential, error) {
	cred, err := store.Resolve(identity)
	if err != nil {
		return nil, err
	}

	oldExpiry := time.UnixMilli(cred.ClaudeAiOauth.ExpiresAt)
	printf("Refreshing %s (%s)...\n", cred.Name, cred.ID[:8])

	tokens, err := oauth.Refresh(cred.ClaudeAiOauth.RefreshToken)
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
			return nil, fmt.Errorf("refresh token expired or revoked. Re-authenticate with `ccm login`")
		}
		return nil, err
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

	if profile := oauth.FetchProfile(cred.ClaudeAiOauth.AccessToken); profile.Tier != "" {
		cred.Subscription.Tier = profile.Tier
	}

	if err := store.Save(cred); err != nil {
		return nil, fmt.Errorf("save credentials: %w", err)
	}

	newExpiry := time.UnixMilli(cred.ClaudeAiOauth.ExpiresAt)
	printf("Refreshed. Old expiry: %s → New expiry: %s\n",
		oldExpiry.Local().Format("15:04:05"),
		newExpiry.Local().Format("15:04:05"),
	)

	return cred, nil
}

type refreshResult struct {
	index int
	buf   string
	cred  *store.Credential
	err   error
}

func refreshAllCredentials() error {
	creds, err := store.List()
	if err != nil {
		return err
	}
	if len(creds) == 0 {
		return fmt.Errorf("no credentials found")
	}

	results := make([]refreshResult, len(creds))
	var wg sync.WaitGroup

	for i, cred := range creds {
		wg.Add(1)
		go func(i int, cred *store.Credential) {
			defer wg.Done()
			var buf bytes.Buffer
			printf := func(format string, a ...any) (int, error) {
				return fmt.Fprintf(&buf, format, a...)
			}
			refreshed, err := doRefreshCredential(cred.ID, printf)
			results[i] = refreshResult{index: i, buf: buf.String(), cred: refreshed, err: err}
		}(i, cred)
	}
	wg.Wait()

	var failed int
	for _, r := range results {
		fmt.Print(r.buf)
		if r.err != nil {
			fmt.Printf("Error: %v\n", r.err)
			failed++
		} else if claude.IsActive(r.cred.ID) {
			if err := claude.WriteActive(r.cred); err != nil {
				fmt.Printf("Warning: could not update active credential: %v\n", err)
			} else {
				fmt.Println("Active credential updated.")
			}
		}
		fmt.Println()
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d credentials failed to refresh", failed, len(creds))
	}
	return nil
}
