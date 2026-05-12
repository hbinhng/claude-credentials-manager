package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

// readPinnedTokenFromEnv returns the trimmed CCM_SHARE_TOKEN value,
// or empty string if unset / whitespace-only. Validation lives in
// share.StartSession; this helper is only the env-read shim.
func readPinnedTokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("CCM_SHARE_TOKEN"))
}

// wrapPinnedTokenErr re-wraps a share.StartSession error so the
// operator sees the env-var name when the failure is a pinned-token
// validation error. Other errors pass through unchanged.
func wrapPinnedTokenErr(err error) error {
	if err != nil && errors.Is(err, share.ErrInvalidPinnedToken) {
		return fmt.Errorf("CCM_SHARE_TOKEN: %w", err)
	}
	return err
}

var (
	shareModelAliases   []string
	shareMaxConcurrency int
)

func init() {
	rootCmd.AddCommand(shareCmd)
	shareCmd.Flags().String("prompt", share.DefaultCapturePrompt, "prompt passed to `claude -p` during identity capture")
	shareCmd.Flags().String("bind-host", "", "host/IP the remote side will dial (goes into the ticket); presence skips the Cloudflare tunnel and makes the listener LAN-reachable")
	shareCmd.Flags().Int("bind-port", 0, "pinned TCP port for the proxy listener (default: OS-assigned); works with or without --bind-host")
	shareCmd.Flags().Bool("load-balance", false, "pool every credential and rotate every --rebalance-interval based on quota feasibility")
	shareCmd.Flags().Duration("rebalance-interval", 5*time.Minute, "tick interval for load-balance rotation (min 30s, max 1h); only meaningful with --load-balance")
	shareCmd.Flags().StringArrayVar(&shareModelAliases, "model-alias", nil,
		"model alias rule like 'claude-opus-*=gpt-5-codex' (repeatable)")
	shareCmd.Flags().IntVar(&shareMaxConcurrency, "max-concurrency", 3,
		"per-credential in-flight request limit (0 = no limit)")
	shareCmd.Flags().StringArray("passthrough", nil,
		"another ccm share's base64 ticket to include as a pool member (repeatable)")
	shareCmd.PreRunE = requireOnline
}

// validateRebalanceInterval rejects out-of-range values for the
// --rebalance-interval flag. Used by both the CLI handler and the
// flag-test suite.
func validateRebalanceInterval(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("--rebalance-interval %q: %w", s, err)
	}
	return validateRebalanceDuration(d)
}

// validateRebalanceDuration is the duration-typed core that
// validateRebalanceInterval and the CLI handler both call.
func validateRebalanceDuration(d time.Duration) error {
	if d < 30*time.Second {
		return fmt.Errorf("--rebalance-interval must be >= 30s, got %s", d)
	}
	if d > time.Hour {
		return fmt.Errorf("--rebalance-interval must be <= 1h, got %s", d)
	}
	return nil
}

// validateShareArgs is the cobra Args function for `ccm share`.
// It enforces "at most one total (local + passthrough) unless --load-balance".
// With --load-balance and zero explicit args/passthroughs, the pool
// is sourced from all creds in the store (legacy implicit-pool behavior).
// Directly testable without a running Cobra command tree.
func validateShareArgs(cmd *cobra.Command, args []string) error {
	loadBalance, _ := cmd.Flags().GetBool("load-balance")
	passthrough, _ := cmd.Flags().GetStringArray("passthrough")
	localCount := len(args)
	ptCount := len(passthrough)
	total := localCount + ptCount

	// --load-balance with zero explicit args/passthroughs = implicit pool
	// (all creds in store). Always valid.
	if loadBalance {
		return nil
	}

	if total == 0 {
		return errors.New("requires a credential, --passthrough, or --load-balance")
	}
	if total > 1 {
		return fmt.Errorf("--load-balance is required when more than one credential or passthrough is provided (got %d local + %d passthrough)", localCount, ptCount)
	}
	return nil
}

var shareCmd = &cobra.Command{
	Use:   "share [<id-or-name>...]",
	Short: "Expose one or more credentials over a Cloudflare Quick Tunnel",
	Args:  validateShareArgs,
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		// In load-balance multi-arg mode, suggest creds not yet on the
		// command line.
		loadBalance, _ := cmd.Flags().GetBool("load-balance")
		results, dir := completeCredential(cmd, args, toComplete)
		if !loadBalance {
			return results, dir
		}
		seen := make(map[string]struct{}, len(args))
		for _, a := range args {
			seen[a] = struct{}{}
		}
		filtered := results[:0]
		for _, r := range results {
			if _, dup := seen[r]; dup {
				continue
			}
			filtered = append(filtered, r)
		}
		return filtered, dir
	},
	Long: `Start an HTTP reverse proxy and a Cloudflare Quick Tunnel so that a
remote Claude Code instance can use this ccm-managed credential without
it ever leaving this machine.

With --load-balance, pool every available credential (or every named
credential) and rotate between them every --rebalance-interval based
on a feasibility formula derived from the Anthropic usage API.

Set CCM_SHARE_TOKEN to pin the inbound access token across restarts
(must match [A-Za-z0-9_-]+); unset/empty mints a fresh random token
per session.

The share session stays alive until you press Ctrl-C.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt, _ := cmd.Flags().GetString("prompt")
		bindHost, _ := cmd.Flags().GetString("bind-host")
		bindPort, _ := cmd.Flags().GetInt("bind-port")
		loadBalance, _ := cmd.Flags().GetBool("load-balance")
		rebalanceInterval, _ := cmd.Flags().GetDuration("rebalance-interval")

		aliasMap, err := alias.Parse(shareModelAliases)
		if err != nil {
			return fmt.Errorf("parse --model-alias: %w", err)
		}

		pinnedToken := readPinnedTokenFromEnv()

		passthroughTickets, _ := cmd.Flags().GetStringArray("passthrough")

		var passthroughSeeds []share.PassthroughSeed
		if len(passthroughTickets) > 0 {
			fmt.Fprintln(os.Stderr, "ccm share: warning: passthrough adds a network hop; deeply nested or cyclic chains will degrade performance")
			for i, raw := range passthroughTickets {
				t, err := share.DecodeTicket(raw)
				if err != nil {
					return fmt.Errorf("--passthrough[%d]: %w", i, err)
				}
				seed, perr := share.BootstrapPassthroughProbe(t)
				if perr != nil {
					return fmt.Errorf("--passthrough[%d] (%s): %w", i, t.Host, perr)
				}
				passthroughSeeds = append(passthroughSeeds, seed)
			}
		}

		shareOpts := share.Options{
			BindHost:          bindHost,
			BindPort:          bindPort,
			CapturePrompt:     prompt,
			Debug:             os.Getenv("CCM_SHARE_DEBUG") == "1",
			RebalanceInterval: rebalanceInterval,
			PinnedAccessToken: pinnedToken,
			AliasMap:          aliasMap,
			MaxConcurrency:    shareMaxConcurrency,
		}

		// Single-cred fast path: one local cred, no passthrough, no LB.
		if !loadBalance && len(passthroughSeeds) == 0 && len(args) == 1 {
			cred, err := store.Resolve(args[0])
			if err != nil {
				return err
			}
			return runShareSingle(cred, shareOpts)
		}

		// Multi-or-passthrough path.
		if loadBalance {
			if err := validateRebalanceDuration(rebalanceInterval); err != nil {
				return err
			}
		}
		return runShareMixed(args, passthroughSeeds, shareOpts)
	},
}

func runShareSingle(cred *store.Credential, opts share.Options) error {
	// Pre-flight refresh: rotate access token if expiring soon. Routes
	// through credflow which dispatches per-provider (claude / codex) and
	// handles codex's rotating refresh-token model with file locking.
	if cred.IsExpired() || cred.IsExpiringSoon() {
		fmt.Fprintln(os.Stderr, "Credential is expired or expiring soon — refreshing...")
		refreshed, err := credflow.RefreshFn(cred.ID)
		if err != nil {
			// Non-fatal: continue with the existing token. The share proxy's
			// in-session refresh path will retry. Surface the warning so the
			// operator can diagnose.
			fmt.Fprintf(os.Stderr, "warning: pre-flight refresh failed: %v\n", err)
		} else {
			cred = refreshed
		}
	}

	sess, err := share.StartSession(cred, opts)
	if err != nil {
		return wrapPinnedTokenErr(err)
	}
	return runSessionLoop(sess, cred)
}

// runShareMixed dispatches to share.BuildPoolFromMixed (supports
// local creds + passthrough seeds + any combination) and starts the
// session.
func runShareMixed(localArgs []string, seeds []share.PassthroughSeed, opts share.Options) error {
	localArgs = splitCommaArgs(localArgs)
	pool, initialCred, initialEntry, err := share.BuildPoolFromMixed(localArgs, seeds, opts.CapturePrompt, false)
	if err != nil {
		return err
	}
	opts.Pool = pool
	if initialCred == nil {
		opts.InitialEntryID = initialEntry.State().CredID()
		opts.InitialEntryName = initialEntry.State().CredName()
	}
	sess, err := share.StartSession(initialCred, opts)
	if err != nil {
		return wrapPinnedTokenErr(err)
	}
	registerPoolSnapshotSignal(sess)
	return runSessionLoop(sess, initialCred)
}

func runSessionLoop(sess share.Session, cred *store.Credential) error {
	defer sess.Stop()

	var displayName, idShort string
	if cred != nil {
		displayName = cred.Name
		if displayName == "" {
			displayName = cred.ID[:8]
		}
		idShort = cred.ID[:8]
	} else {
		displayName = sess.CredID()
		idShort = sess.CredID()
	}

	fmt.Println()
	fmt.Printf("Share session for %s (%s) is live.\n", displayName, idShort)
	if sess.Mode() == "lan" {
		fmt.Printf("  reach:   %s (LAN)\n", sess.Reach())
		fmt.Println("  WARNING: listener is LAN-reachable - anyone who can route")
		fmt.Println("           to this machine AND has the ticket can use this")
		fmt.Println("           credential.")
	} else {
		fmt.Printf("  tunnel:  %s\n", sess.Reach())
	}
	fmt.Println()
	fmt.Println("Ticket (give this to the remote side):")
	fmt.Println()
	fmt.Printf("  %s\n", sess.Ticket())
	fmt.Println()
	fmt.Println("On the remote machine, run:")
	fmt.Printf("  ccm launch --via %s\n", sess.Ticket())
	fmt.Println()
	fmt.Println("Press Ctrl-C to stop the share session.")

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigC)

	select {
	case <-sigC:
		fmt.Println()
		fmt.Println("Stopping share session...")
		return nil
	case <-sess.Done():
		return sess.Err()
	}
}

// registerPoolSnapshotSignal is implemented per-platform; on Unix it
// hooks SIGUSR1 to dump the current pool state, on other platforms
// it's a no-op.
var registerPoolSnapshotSignal = func(sess share.Session) { _ = sess }
