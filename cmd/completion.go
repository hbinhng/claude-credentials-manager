package cmd

import (
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

// completeCredential provides shell completions for credential ID or name arguments.
func completeCredential(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Only complete the first positional arg (or second for rename)
	maxArgs := 1
	if cmd.Name() == "rename" {
		maxArgs = 1 // only first arg is id-or-name; second is free-form
	}
	if len(args) >= maxArgs {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	creds, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var suggestions []string
	seen := map[string]bool{}
	for _, c := range creds {
		// Suggest short ID with description
		short := c.ID[:8]
		if !seen[short] {
			suggestions = append(suggestions, short+"\t"+c.Name+" ("+c.Status()+")")
			seen[short] = true
		}
		// Suggest name if different from ID
		if c.Name != c.ID && !seen[c.Name] {
			suggestions = append(suggestions, c.Name+"\t"+short+" ("+c.Status()+")")
			seen[c.Name] = true
		}
	}

	return suggestions, cobra.ShellCompDirectiveNoFileComp
}
