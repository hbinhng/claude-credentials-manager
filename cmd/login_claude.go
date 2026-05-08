package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

// beginLoginFn and completeLoginFn are the cmd-level seams for the
// claude login flow. Tests may replace them without touching credflow
// internals.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
var beginLoginFn = credflow.BeginLogin
var completeLoginFn = credflow.CompleteLogin

// claudeLoginStdinFn is the reader seam for the paste-code prompt.
// Tests override to avoid blocking on real stdin.
var claudeLoginStdinFn func() (io.Reader, error) = func() (io.Reader, error) {
	return os.Stdin, nil // untestable: default body replaced in every test; real stdin cannot fail to return
}

var loginClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Capture an Anthropic OAuth credential for Claude Code",
	RunE:  runLoginClaude,
}

func runLoginClaude(cmd *cobra.Command, args []string) error {
	hs, err := beginLoginFn()
	if err != nil {
		return err
	}

	fmt.Println("\nOpen this URL in your browser to authenticate:")
	fmt.Printf("\n  %s\n\n", hs.AuthorizeURL)
	tryOpenBrowserFn(hs.AuthorizeURL)

	fmt.Print("Paste the code here: ")
	r, err := claudeLoginStdinFn()
	if err != nil {
		return fmt.Errorf("open stdin: %w", err)
	}
	reader := bufio.NewReader(r)
	raw, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read code: %w", err)
	}
	code := strings.TrimSpace(raw)
	if code == "" {
		return fmt.Errorf("no code provided")
	}

	fmt.Println("Exchanging code for tokens...")
	cred, err := completeLoginFn(hs, code)
	if err != nil {
		return err
	}

	fmt.Printf("\nLogged in as %s\n", cred.Name)
	if cred.Name == cred.ID {
		fmt.Printf("Use `ccm rename %s <name>` to set a friendly name.\n", cred.ID[:8])
	}
	return nil
}

// SeamClaudeLogin replaces both credflow entry points and the stdin
// reader at once. Returns a cleanup that restores all three. Test-only.
//
// NOT goroutine-safe. Tests that mutate must NOT call t.Parallel().
func SeamClaudeLogin(
	begin func() (*credflow.Handshake, error),
	complete func(*credflow.Handshake, string) (*store.Credential, error),
	stdin io.Reader,
) func() {
	prevBegin := beginLoginFn
	prevComplete := completeLoginFn
	prevStdin := claudeLoginStdinFn
	beginLoginFn = begin
	completeLoginFn = complete
	claudeLoginStdinFn = func() (io.Reader, error) { return stdin, nil }
	return func() {
		beginLoginFn = prevBegin
		completeLoginFn = prevComplete
		claudeLoginStdinFn = prevStdin
	}
}
