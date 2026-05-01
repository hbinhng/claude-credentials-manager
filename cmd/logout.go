package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/claude"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

// logoutRestoreFn is the seam tests override; production points at claude.Restore.
var logoutRestoreFn = claude.Restore

func init() {
	logoutCmd.Flags().BoolP("force", "f", false, "Skip confirmation for active credential")
	rootCmd.AddCommand(logoutCmd)
}

var logoutCmd = &cobra.Command{
	Use:               "logout <id-or-name>",
	Short:             "Remove a credential",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeCredential,
	RunE: func(cmd *cobra.Command, args []string) error {
		cred, err := store.Resolve(args[0])
		if err != nil {
			return err
		}
		force, _ := cmd.Flags().GetBool("force")
		if claude.IsActive(cred.ID) && !force {
			fmt.Printf("Credential %s (%s) is currently active.\n", cred.Name, cred.ID[:8])
			fmt.Print("Remove anyway? [y/N] ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				fmt.Println("Aborted.")
				return nil
			}
		}
		if err := doLogout(cred.ID); err != nil {
			return err
		}
		fmt.Printf("Removed %s (%s)\n", cred.Name, cred.ID[:8])
		return nil
	},
}

// doLogout removes the credential from the store. If it was the active
// one, Restore is called first (best-effort: restore failure is logged
// but does not abort the delete — the user can `ccm restore` manually).
func doLogout(id string) error {
	if claude.IsActive(id) {
		if err := logoutRestoreFn(); err != nil {
			fmt.Fprintf(os.Stderr, "ccm: restore failed: %v\n", err)
		}
	}
	if err := store.Delete(id); err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}
