package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

var codexLoginFn = codexoauth.Login

// SeamCodexLogin replaces the codex login orchestrator. Returns a
// cleanup that restores the original. Test-only.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
func SeamCodexLogin(fn func(context.Context, io.Writer, io.Reader) (*store.Credential, error)) func() {
	prev := codexLoginFn
	codexLoginFn = fn
	return func() { codexLoginFn = prev }
}

var loginCodexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Capture an OpenAI/ChatGPT OAuth credential for the codex CLI",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		cred, err := codexLoginFn(ctx, cmd.OutOrStdout(), os.Stdin)
		if err != nil {
			return err
		}
		if err := store.Save(cred); err != nil {
			return fmt.Errorf("save credential: %w", err)
		}
		idShort := cred.ID
		if len(idShort) > 8 {
			idShort = idShort[:8]
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s (id: %s)\n", cred.Name, idShort)
		return nil
	},
}
