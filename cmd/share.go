package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(shareCmd)
	shareCmd.Flags().String("prompt", share.DefaultCapturePrompt, "prompt passed to `claude -p` during identity capture")
	shareCmd.Flags().String("bind-host", "", "host/IP the remote side will dial (goes into the ticket); presence skips the Cloudflare tunnel and makes the listener LAN-reachable")
	shareCmd.Flags().Int("bind-port", 0, "pinned TCP port for the proxy listener (default: OS-assigned); works with or without --bind-host")
	shareCmd.Flags().Bool("load-balance", false, "pool every credential and rotate every --rebalance-interval based on quota feasibility")
	shareCmd.Flags().Duration("rebalance-interval", 5*time.Minute, "tick interval for load-balance rotation (min 30s, max 1h); only meaningful with --load-balance")
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
// It enforces "exactly one arg unless --load-balance" and is also
// directly testable.
func validateShareArgs(cmd *cobra.Command, args []string) error {
	loadBalance, _ := cmd.Flags().GetBool("load-balance")
	if loadBalance {
		return cobra.ArbitraryArgs(cmd, args)
	}
	return cobra.ExactArgs(1)(cmd, args)
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

The share session stays alive until you press Ctrl-C.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt, _ := cmd.Flags().GetString("prompt")
		bindHost, _ := cmd.Flags().GetString("bind-host")
		bindPort, _ := cmd.Flags().GetInt("bind-port")
		loadBalance, _ := cmd.Flags().GetBool("load-balance")
		rebalanceInterval, _ := cmd.Flags().GetDuration("rebalance-interval")

		if loadBalance {
			if err := validateRebalanceDuration(rebalanceInterval); err != nil {
				return err
			}
			return runShareLoadBalance(args, share.Options{
				BindHost:          bindHost,
				BindPort:          bindPort,
				CapturePrompt:     prompt,
				Debug:             os.Getenv("CCM_SHARE_DEBUG") == "1",
				RebalanceInterval: rebalanceInterval,
			})
		}

		cred, err := store.Resolve(args[0])
		if err != nil {
			return err
		}
		return runShareSingle(cred, share.Options{
			BindHost:      bindHost,
			BindPort:      bindPort,
			CapturePrompt: prompt,
			Debug:         os.Getenv("CCM_SHARE_DEBUG") == "1",
		})
	},
}

func runShareSingle(cred *store.Credential, opts share.Options) error {
	sess, err := share.StartSession(cred, opts)
	if err != nil {
		return err
	}
	return runSessionLoop(sess, cred)
}

func runShareLoadBalance(args []string, opts share.Options) error {
	pool, initialCred, err := share.BuildPool(args, opts.CapturePrompt, false)
	if err != nil {
		return err
	}
	opts.Pool = pool
	sess, err := share.StartSession(initialCred, opts)
	if err != nil {
		return err
	}
	registerPoolSnapshotSignal(sess)
	return runSessionLoop(sess, initialCred)
}

func runSessionLoop(sess share.Session, cred *store.Credential) error {
	defer sess.Stop()

	displayName := cred.Name
	if displayName == "" {
		displayName = cred.ID[:8]
	}

	fmt.Println()
	fmt.Printf("Share session for %s (%s) is live.\n", displayName, cred.ID[:8])
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
