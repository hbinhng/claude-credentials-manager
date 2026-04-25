package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.PreRunE = requireOnline
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate a new Claude account via OAuth",
	RunE: func(cmd *cobra.Command, args []string) error {
		hs, err := credflow.BeginLogin()
		if err != nil {
			return err
		}

		fmt.Println("\nOpen this URL in your browser to authenticate:")
		fmt.Printf("\n  %s\n\n", hs.AuthorizeURL)
		tryOpenBrowserFn(hs.AuthorizeURL)

		fmt.Print("Paste the code here: ")
		reader := bufio.NewReader(os.Stdin)
		raw, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read code: %w", err)
		}
		code := strings.TrimSpace(raw)
		if code == "" {
			return fmt.Errorf("no code provided")
		}

		fmt.Println("Exchanging code for tokens...")
		cred, err := credflow.CompleteLogin(hs, code)
		if err != nil {
			return err
		}

		fmt.Printf("\nLogged in as %s\n", cred.Name)
		if cred.Name == cred.ID {
			fmt.Printf("Use `ccm rename %s <name>` to set a friendly name.\n", cred.ID[:8])
		}
		return nil
	},
}
