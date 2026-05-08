package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

var launchModelAliases []string

func init() {
	rootCmd.AddCommand(launchCmd)
	launchCmd.Flags().String("via", "", "ticket emitted by `ccm share` on the host side (remote mode)")
	launchCmd.Flags().Bool("load-balance", false, "pool every credential and rotate every --rebalance-interval based on quota feasibility")
	launchCmd.Flags().Duration("rebalance-interval", 5*time.Minute, "tick interval for load-balance rotation (min 30s, max 1h); only meaningful with --load-balance")
	launchCmd.Flags().StringArrayVar(&launchModelAliases, "model-alias", nil,
		"model alias rule like 'claude-opus-*=gpt-5-codex' (repeatable)")
	launchCmd.Args = validateLaunchArgs
	launchCmd.PreRunE = requireOnline
}

// validateLaunchArgs enforces the three-mode mutual exclusivity for
// `ccm launch`:
//
//   - single-cred:  exactly one positional id (--via empty, --load-balance false)
//   - via:          zero positional ids (--via set,  --load-balance false)
//   - load-balance: zero or more positional ids (--load-balance true, --via empty)
//
// Positional args after `--` are forwarded to the spawned claude
// process; they are excluded from the count via cmd.ArgsLenAtDash.
func validateLaunchArgs(cmd *cobra.Command, args []string) error {
	via, _ := cmd.Flags().GetString("via")
	loadBalance, _ := cmd.Flags().GetBool("load-balance")

	// Strip post-dash args (those go to claude). When ArgsLenAtDash
	// returns -1 the user supplied no `--`; the whole slice is
	// pre-dash.
	beforeDash := args
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		beforeDash = args[:n]
	}

	if loadBalance && via != "" {
		return errors.New("cannot specify both --load-balance and --via")
	}
	if loadBalance {
		// Any number of positional args (zero or more).
		return nil
	}
	if via != "" {
		if len(beforeDash) != 0 {
			return errors.New("cannot specify positional credential with --via")
		}
		return nil
	}
	if len(beforeDash) != 1 {
		return errors.New("requires exactly one credential, --via, or --load-balance")
	}
	return nil
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
		loadBalance, _ := cmd.Flags().GetBool("load-balance")
		rebalanceInterval, _ := cmd.Flags().GetDuration("rebalance-interval")

		// Split positional args into "ours" (before --) and "claude's"
		// (after --). cobra exposes ArgsLenAtDash for exactly this.
		var beforeDash, afterDash []string
		if n := cmd.ArgsLenAtDash(); n >= 0 {
			beforeDash = args[:n]
			afterDash = args[n:]
		} else {
			beforeDash = args
		}

		// validateLaunchArgs (wired as launchCmd.Args) has already
		// enforced the mutual-exclusivity invariants by the time we
		// get here.
		if loadBalance {
			if err := validateRebalanceDuration(rebalanceInterval); err != nil {
				return err
			}
			return runLaunchLoadBalance(beforeDash, afterDash, rebalanceInterval)
		}
		if via != "" {
			return runLaunchRemote(via, afterDash)
		}
		return runLaunchLocal(beforeDash[0], afterDash)
	},
}

// Note: ccm launch performs no status-code-specific retry. The
// `claude` child handles its own retries. Specifically, neither 502
// nor 503 are retried at this layer, so the ccm share --load-balance
// 503 path requires no changes here. Confirmed during Task 18.

// runLaunchLocal starts a loopback passthrough proxy for the named
// credential and execs claude pointed at it.
func runLaunchLocal(idOrName string, claudeArgs []string) error {
	cred, err := store.Resolve(idOrName)
	if err != nil {
		return err
	}

	// Codex provider: hard-fail with install hint when codex CLI is
	// absent. Capture is impossible without it (the CLI is spawned to
	// record its identity headers). This check runs before any session
	// setup so the error is immediate and actionable.
	if cred.ProviderName() == "codex" {
		if _, err := exec.LookPath("codex"); err != nil {
			return fmt.Errorf("codex CLI is required for this command. " +
				"Install it from https://github.com/openai/codex; ccm uses it " +
				"to capture identity headers for the codex backend")
		}
	}

	// Parse --model-alias flag values into an alias map. Boot-time
	// conflict detection in alias.Parse rejects overlapping patterns.
	aliasMap, err := alias.Parse(launchModelAliases)
	if err != nil {
		return fmt.Errorf("parse --model-alias: %w", err)
	}

	// Pre-flight refresh: rotate access token if expiring soon. Routes
	// through credflow which dispatches per-provider (claude / codex) and
	// handles codex's rotating refresh-token model with file locking.
	if cred.IsExpired() || cred.IsExpiringSoon() {
		fmt.Fprintln(os.Stderr, "Credential is expired or expiring soon — refreshing...")
		refreshed, err := credflow.RefreshFn(cred.ID)
		if err != nil {
			// Non-fatal: continue with the existing token. Subsequent
			// in-session refresh in credstate.go will retry. But surface
			// the error to the user for diagnosis.
			fmt.Fprintf(os.Stderr, "warning: pre-flight refresh failed: %v\n", err)
		} else {
			cred = refreshed
		}
	}

	proxy, err := share.NewLocalProxy(cred)
	if err != nil {
		return fmt.Errorf("start local proxy: %w", err)
	}
	proxy.SetAliasMap(aliasMap)
	defer proxy.Close()

	proxyErrC := make(chan error, 1)
	go func() { proxyErrC <- proxy.Start() }()

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("could not find 'claude' on PATH")
	}

	// Build the child environment: inherit the parent's env minus
	// any Anthropic auth vars that would override claude-cli's
	// keychain-OAuth code path and break the passthrough proxy's
	// Anthropic-Beta expectations. Also strip ANTHROPIC_BASE_URL so
	// our own value is the only one the child sees (otherwise an
	// inherited override could win when the test or wrapping shell
	// pre-populated it).
	env := filterEnv(os.Environ(),
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
	)
	env = append(env, "ANTHROPIC_BASE_URL="+proxy.Addr())

	if err := share.LaunchExecFn()(claudeBin, claudeArgs, env); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

// runLaunchLoadBalance builds a multi-cred load-balance pool, points
// a LocalProxy at it, and execs claude through that proxy. Mirrors
// `ccm share --load-balance` minus the per-cred capture phase: the
// spawned claude provides its own outbound headers, so the pool's
// activatedHeaders are never replayed.
func runLaunchLoadBalance(args []string, claudeArgs []string, rebalanceInterval time.Duration) error {
	debug := os.Getenv("CCM_LAUNCH_DEBUG") == "1" || os.Getenv("CCM_SHARE_DEBUG") == "1"

	args = splitCommaArgs(args)
	pool, _, err := share.BuildPool(args, "", true)
	if err != nil {
		return err
	}

	proxy, err := share.NewLocalProxyWithPool(pool, debug)
	if err != nil {
		// coverage: unreachable — NewLocalProxyWithPool only errors
		// on nil pool or net.Listen failure on 127.0.0.1:0; neither
		// is exercisable in unit tests.
		return fmt.Errorf("start local proxy: %w", err)
	}
	defer proxy.Close()

	go func() { _ = proxy.Start() }()

	if err := share.StartPoolBackground(proxy.Done(), pool, share.PoolBackgroundOptions{
		RebalanceInterval: rebalanceInterval,
		Debug:             debug,
		SkipCapture:       true,
	}); err != nil {
		return err
	}

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("could not find 'claude' on PATH")
	}

	env := filterEnv(os.Environ(),
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
	)
	env = append(env, "ANTHROPIC_BASE_URL="+proxy.Addr())

	if err := share.LaunchExecFn()(claudeBin, claudeArgs, env); err != nil {
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
