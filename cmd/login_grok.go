package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	grokoauth "github.com/hbinhng/claude-credentials-manager/internal/grok/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

var grokLoginFn = grokoauth.Login

// SeamGrokLogin replaces the grok login orchestrator. Returns a cleanup
// that restores the original. Test-only.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
func SeamGrokLogin(fn func(context.Context, io.Writer, io.Reader) (*store.Credential, error)) func() {
	prev := grokLoginFn
	grokLoginFn = fn
	return func() { grokLoginFn = prev }
}

var loginGrokCmd = &cobra.Command{
	Use:   "grok",
	Short: "Capture an xAI/Grok OAuth credential (SuperGrok / X Premium+)",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		cred, err := grokLoginFn(ctx, cmd.OutOrStdout(), os.Stdin)
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
