package cmd

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

var refreshAll bool

func init() {
	refreshCmd.Flags().BoolVarP(&refreshAll, "all", "a", false, "Refresh all credentials")
	rootCmd.AddCommand(refreshCmd)
	refreshCmd.PreRunE = requireOnline
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

// Seams so tests can exercise the refreshCredential active-credential
// branches without needing a real ~/.claude/.credentials.json setup.
var (
	claudeIsActiveFn    = claude.IsActive
	claudeWriteActiveFn = claude.WriteActive
)

func refreshCredential(identity string) error {
	cred, err := doRefreshCredential(identity, fmt.Printf)
	if err != nil {
		return err
	}

	if claudeIsActiveFn(cred.ID) {
		if err := claudeWriteActiveFn(cred); err != nil {
			fmt.Printf("Warning: could not update active credential: %v\n", err)
		} else {
			fmt.Println("Active credential updated.")
		}
	}

	return nil
}

// refreshCredentialFn is the seam tests override to skip real OAuth
// calls. Production points at credflow.RefreshCredential.
var refreshCredentialFn = credflow.RefreshCredential

func doRefreshCredential(identity string, printf func(string, ...any) (int, error)) (*store.Credential, error) {
	// Resolve fuzzy inputs (prefixes, names) here so the shared
	// credflow.RefreshCredential helper can stay ID-only.
	resolved, err := store.Resolve(identity)
	if err != nil {
		return nil, err
	}
	oldExpiry := time.UnixMilli(resolved.ClaudeAiOauth.ExpiresAt)
	printf("Refreshing %s (%s)...\n", resolved.Name, resolved.ID[:8])

	cred, err := refreshCredentialFn(resolved.ID)
	if err != nil {
		return nil, err
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
