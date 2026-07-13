package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/hbinhng/claude-credentials-manager/internal/codex"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

// codexIsActiveFn / codexWriteActiveFn mirror the claude active seams in
// refresh.go so relogin's active-copy sync is testable without real
// activation. Production: codex.IsActive / codex.WriteActive.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
var (
	codexIsActiveFn    = codex.IsActive
	codexWriteActiveFn = codex.WriteActive
)

var reloginCmd = &cobra.Command{
	Use:   "relogin <id|name>",
	Short: "Re-authenticate an existing credential in place (keeps its id and name)",
	Long: `relogin re-runs the OAuth flow for an existing credential's provider and
grafts the fresh tokens back onto the SAME credential — its id, name, and
creation time are preserved.

Use it when a refresh token has been revoked or expired and ` + "`ccm refresh`" + `
can no longer rotate it. Unlike ` + "`ccm login`" + ` (which mints a brand-new
credential with a new id), relogin keeps the credential's identity, so an
active credential stays active and any share/serve pool membership is
undisturbed.`,
	Args: cobra.ExactArgs(1),
	RunE: runRelogin,
}

func init() {
	rootCmd.AddCommand(reloginCmd)
	// relogin hits auth.anthropic.com / auth.openai.com / auth.x.ai.
	reloginCmd.PreRunE = requireOnline
}

func runRelogin(cmd *cobra.Command, args []string) error {
	existing, err := store.Resolve(args[0])
	if err != nil {
		return err
	}
	provider := existing.ProviderName()
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	fmt.Fprintf(out, "Re-authenticating %s (%s, id: %s)...\n",
		existing.Name, provider, reloginShortID(existing.ID))

	var fresh *store.Credential
	switch provider {
	case "claude":
		fresh, err = captureClaudeLogin(cmd)
	case "codex":
		fresh, err = codexLoginFn(ctx, out, os.Stdin)
	case "grok":
		fresh, err = grokLoginFn(ctx, out, os.Stdin)
	default:
		// untestable: store.UnmarshalJSON rejects unknown providers, so a
		// credential carrying one can never be resolved and reach here.
		return fmt.Errorf("relogin: unknown provider %q", provider)
	}
	if err != nil {
		return err
	}

	// The login flows mint a throwaway UUID; claude's CompleteLogin also
	// persists a throwaway file. Graft the fresh tokens onto the existing
	// identity so the credential is re-authenticated in place.
	mintedID := fresh.ID
	fresh.ID = existing.ID
	fresh.Name = existing.Name
	fresh.CreatedAt = existing.CreatedAt
	if err := store.Save(fresh); err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	if mintedID != existing.ID {
		// Best-effort: codex/grok never wrote a file at the minted id (Delete
		// is a no-op there); claude's CompleteLogin did — remove that orphan.
		_ = store.Delete(mintedID)
	}

	// Keep any active copy in sync so the re-auth takes effect immediately.
	switch provider {
	case "claude":
		if claudeIsActiveFn(fresh.ID) {
			if err := claudeWriteActiveFn(fresh); err != nil {
				return fmt.Errorf("sync active credential: %w", err)
			}
		}
	case "codex":
		if codexIsActiveFn(fresh.ID) {
			if err := codexWriteActiveFn(fresh); err != nil {
				return fmt.Errorf("sync active credential: %w", err)
			}
		}
		// grok is proxy-only — no activation to sync.
	}

	fmt.Fprintf(out, "Re-authenticated %s (id: %s)\n", fresh.Name, reloginShortID(fresh.ID))
	return nil
}

func reloginShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
