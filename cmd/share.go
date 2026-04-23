package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(shareCmd)
	shareCmd.Flags().String("prompt", share.DefaultCapturePrompt, "prompt passed to `claude -p` during identity capture")
	shareCmd.Flags().String("bind-host", "", "host/IP the remote side will dial (goes into the ticket); presence skips the Cloudflare tunnel and makes the listener LAN-reachable")
	shareCmd.Flags().Int("bind-port", 0, "pinned TCP port for the proxy listener (default: OS-assigned); works with or without --bind-host")
	shareCmd.PreRunE = requireOnline
}

var shareCmd = &cobra.Command{
	Use:               "share <id-or-name>",
	Short:             "Expose a credential over a Cloudflare Quick Tunnel",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeCredential,
	Long: `Start an HTTP reverse proxy and a Cloudflare Quick Tunnel so that a
remote Claude Code instance can use this ccm-managed credential without
it ever leaving this machine.

The command does four things:

  1. Launches a local reverse proxy in CAPTURE mode.
  2. Spawns ` + "`claude -p`" + ` once against the local proxy so the identity
     headers (User-Agent, X-Stainless-*, Anthropic-Version/Beta, ...) of
     the local Claude Code install can be recorded. Subsequent forwarded
     requests replay these headers so the upstream sees a consistent
     caller regardless of which machine the inbound request came from.
  3. Transitions the proxy into SERVING mode, mints a random access
     token, and exposes it via a Cloudflare Quick Tunnel.
  4. Prints a base64-encoded ticket. Feed it to the remote side with
     ` + "`ccm launch --via <ticket>`" + ` or, equivalently, point ` + "`claude`" + ` on
     the remote side at the tunnel URL with the access token as its
     bearer.

The share session stays alive until you press Ctrl-C.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt, _ := cmd.Flags().GetString("prompt")
		bindHost, _ := cmd.Flags().GetString("bind-host")
		bindPort, _ := cmd.Flags().GetInt("bind-port")

		cred, err := store.Resolve(args[0])
		if err != nil {
			return err
		}

		opts := share.Options{
			BindHost:      bindHost,
			BindPort:      bindPort,
			CapturePrompt: prompt,
			Debug:         os.Getenv("CCM_SHARE_DEBUG") == "1",
		}
		sess, err := share.StartSession(cred, opts)
		if err != nil {
			return err
		}
		defer sess.Stop()

		displayName := cred.Name
		if displayName == "" {
			displayName = cred.ID[:8]
		}

		fmt.Println()
		fmt.Printf("Share session for %s (%s) is live.\n", displayName, cred.ID[:8])
		if sess.Mode() == "lan" {
			fmt.Printf("  reach:   %s (LAN)\n", sess.Reach())
			fmt.Println("  WARNING: listener is LAN-reachable — anyone who can route")
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
	},
}
