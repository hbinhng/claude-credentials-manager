package cmd

import (
	"fmt"
	"regexp"

	"github.com/hbinhng/ccm/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(renameCmd)
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

var renameCmd = &cobra.Command{
	Use:   "rename <id-or-name> <new-name>",
	Short: "Rename a credential",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cred, err := store.Resolve(args[0])
		if err != nil {
			return err
		}

		newName := args[1]
		if !namePattern.MatchString(newName) {
			return fmt.Errorf("invalid name %q: use 1-32 alphanumeric, hyphen, or underscore characters", newName)
		}

		// Check for collision
		all, err := store.List()
		if err != nil {
			return err
		}
		for _, c := range all {
			if c.ID == cred.ID {
				continue
			}
			if c.Name == newName || c.ID == newName {
				return fmt.Errorf("name %q already in use by %s", newName, c.ID[:8])
			}
		}

		oldName := cred.Name
		cred.Name = newName
		if err := store.Save(cred); err != nil {
			return fmt.Errorf("save: %w", err)
		}

		fmt.Printf("Renamed %s → %s (%s)\n", oldName, newName, cred.ID[:8])
		return nil
	},
}
