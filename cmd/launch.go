package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(launchCmd)
	launchCmd.Flags().String("via", "", "ticket emitted by `ccm share` on the host side (remote mode)")
	launchCmd.PreRunE = requireOnline
}

var launchCmd = &cobra.Command{
	Use:   "launch [<id-or-name> | --via <ticket>] [-- <claude args>]",
	Short: "Launch Claude Code against a ccm credential without switching the active one",
	Long: `Run Claude Code with a specific ccm-managed credential, either from
the local store or from a remote share ticket, without mutating
~/.claude/.credentials.json.

Two modes are supported:

  ccm launch <id-or-name>            # local: run claude against a local
                                     # passthrough proxy that injects the
                                     # named credential's OAuth bearer

  ccm launch --via <ticket>          # remote: decode a ticket emitted
                                     # by ` + "`ccm share`" + ` and run claude
                                     # against that tunnel

In local mode, ccm starts a loopback reverse proxy, refreshes the
target credential if it is expired or expiring soon, and execs
` + "`claude`" + ` with ANTHROPIC_BASE_URL pointing at the proxy. The child
claude uses its usual keychain-OAuth code path; the proxy strips its
keychain Authorization and injects the target credential's real
bearer. This lets you run multiple claude sessions against different
credentials simultaneously without running ` + "`ccm use`" + `. Requires
~/.claude/.credentials.json to exist (any credential — its bearer is
overwritten); if you have never run ` + "`ccm use`" + `, do that once first.

In remote mode, the ticket carries the tunnel host and an access
token, both of which are forwarded to claude via ANTHROPIC_BASE_URL
and ANTHROPIC_AUTH_TOKEN. No local credential is required; the
bearer comes from the ticket.

Any arguments after ` + "`--`" + ` are passed to ` + "`claude`" + ` unchanged, so you
can use ` + "`ccm launch <id> -- -p 'hi'`" + ` for a one-shot query.`,
	DisableFlagsInUseLine: true,
	ValidArgsFunction:     completeCredential,
	RunE: func(cmd *cobra.Command, args []string) error {
		via, _ := cmd.Flags().GetString("via")

		// Split positional args into "ours" (before --) and "claude's"
		// (after --). cobra exposes ArgsLenAtDash for exactly this.
		var beforeDash, afterDash []string
		if n := cmd.ArgsLenAtDash(); n >= 0 {
			beforeDash = args[:n]
			afterDash = args[n:]
		} else {
			beforeDash = args
		}

		switch {
		case via != "" && len(beforeDash) > 0:
			return errors.New("cannot specify both <id-or-name> and --via")
		case via == "" && len(beforeDash) == 0:
			return errors.New("must specify either <id-or-name> or --via")
		case len(beforeDash) > 1:
			return fmt.Errorf("unexpected extra argument: %s", beforeDash[1])
		}

		if via != "" {
			return runLaunchRemote(via, afterDash)
		}
		return runLaunchLocal(beforeDash[0], afterDash)
	},
}

// runLaunchLocal starts a loopback passthrough proxy for the named
// credential and execs claude pointed at it.
func runLaunchLocal(idOrName string, claudeArgs []string) error {
	cred, err := store.Resolve(idOrName)
	if err != nil {
		return err
	}

	// Refresh up front so the very first forwarded request doesn't
	// have to block on an OAuth roundtrip.
	if cred.IsExpired() || cred.IsExpiringSoon() {
		fmt.Fprintln(os.Stderr, "Credential is expired or expiring soon — refreshing...")
		tokens, err := oauth.Refresh(cred.ClaudeAiOauth.RefreshToken)
		if err != nil {
			return fmt.Errorf("refresh: %w", err)
		}
		cred.ClaudeAiOauth.AccessToken = tokens.AccessToken
		if tokens.RefreshToken != "" {
			cred.ClaudeAiOauth.RefreshToken = tokens.RefreshToken
		}
		cred.ClaudeAiOauth.ExpiresAt = time.Now().UnixMilli() + tokens.ExpiresIn*1000
		cred.LastRefreshedAt = time.Now().UTC().Format(time.RFC3339)
		if err := store.Save(cred); err != nil {
			return fmt.Errorf("save refreshed credential: %w", err)
		}
	}

	proxy, err := share.NewLocalProxy(cred)
	if err != nil {
		return fmt.Errorf("start local proxy: %w", err)
	}
	defer proxy.Close()

	proxyErrC := make(chan error, 1)
	go func() { proxyErrC <- proxy.Start() }()

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("could not find 'claude' on PATH")
	}

	child := exec.Command(claudeBin, claudeArgs...)
	// Build the child environment: inherit the parent's env minus
	// any Anthropic auth vars that would override claude-cli's
	// keychain-OAuth code path and break the passthrough proxy's
	// Anthropic-Beta expectations.
	child.Env = filterEnv(os.Environ(),
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
	)
	child.Env = append(child.Env, "ANTHROPIC_BASE_URL="+proxy.Addr())
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	if err := child.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

// runLaunchRemote decodes a ticket and execs claude pointed at the
// remote share tunnel behind it.
func runLaunchRemote(rawTicket string, claudeArgs []string) error {
	ticket, err := share.DecodeTicket(rawTicket)
	if err != nil {
		return fmt.Errorf("invalid ticket: %w", err)
	}

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("could not find 'claude' on PATH")
	}

	child := exec.Command(claudeBin, claudeArgs...)
	child.Env = append(os.Environ(),
		"ANTHROPIC_BASE_URL="+ticket.Scheme+"://"+ticket.Host,
		"ANTHROPIC_AUTH_TOKEN="+ticket.Token,
	)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	if err := child.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

// filterEnv returns a copy of env with any entry whose key matches one
// of the given names removed.
func filterEnv(env []string, drop ...string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, d := range drop {
			if strings.HasPrefix(e, d+"=") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}
