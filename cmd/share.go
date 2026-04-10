package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(shareCmd)
	shareCmd.Flags().String("prompt", share.DefaultCapturePrompt, "prompt passed to `claude -p` during identity capture")
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

		cred, err := store.Resolve(args[0])
		if err != nil {
			return err
		}

		// Refresh up front if the token is close to expiring so the
		// share session doesn't go stale 30 seconds in.
		if cred.IsExpired() || cred.IsExpiringSoon() {
			fmt.Println("Credential is expired or expiring soon — refreshing...")
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

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		proxy, err := share.NewProxy()
		if err != nil {
			return fmt.Errorf("start proxy: %w", err)
		}
		defer proxy.Close()

		proxyErrC := make(chan error, 1)
		go func() { proxyErrC <- proxy.Start() }()

		// Step 1: capture identity headers from the local claude install.
		fmt.Println("Capturing Claude Code identity headers (this may take a few seconds)...")
		if err := share.RunCapture(ctx, proxy, prompt); err != nil {
			return fmt.Errorf("capture: %w", err)
		}

		// Step 2: mint access token, transition proxy into serving.
		accessToken, err := share.NewRandomToken()
		if err != nil {
			return err
		}
		if err := proxy.Transition(accessToken, cred); err != nil {
			return fmt.Errorf("transition proxy: %w", err)
		}

		// Step 3: stand up the Cloudflare Quick Tunnel.
		fmt.Println("Starting Cloudflare Quick Tunnel...")
		tunnel, err := share.StartTunnel(ctx, proxy.Addr())
		if err != nil {
			return fmt.Errorf("start tunnel: %w", err)
		}
		defer tunnel.Close()

		// Cloudflare prints the tunnel URL before the edge has the route.
		// Poll healthz until the URL actually resolves so the "live"
		// banner is not a lie.
		fmt.Println("Waiting for tunnel to become reachable...")
		if err := tunnel.WaitReady(ctx, 60*time.Second); err != nil {
			return fmt.Errorf("tunnel readiness: %w", err)
		}

		// Step 4: print the ticket.
		ticket := share.Ticket{
			Token: accessToken,
			Host:  trimScheme(tunnel.URL),
		}
		fmt.Println()
		fmt.Printf("Share session for %s (%s) is live.\n", cred.Name, cred.ID[:8])
		fmt.Printf("  tunnel:  %s\n", tunnel.URL)
		fmt.Println()
		fmt.Println("Ticket (give this to the remote side):")
		fmt.Println()
		fmt.Printf("  %s\n", ticket.Encode())
		fmt.Println()
		fmt.Println("On the remote machine, run:")
		fmt.Printf("  ccm launch --via %s\n", ticket.Encode())
		fmt.Println()
		fmt.Println("Press Ctrl-C to stop the share session.")

		select {
		case <-ctx.Done():
			fmt.Println()
			fmt.Println("Stopping share session...")
			return nil
		case err := <-proxyErrC:
			return fmt.Errorf("proxy exited: %w", err)
		}
	},
}

// trimScheme strips "https://" from the front of a URL so the host is
// usable in the ticket's user-info form.
func trimScheme(u string) string {
	const pfx = "https://"
	if len(u) >= len(pfx) && u[:len(pfx)] == pfx {
		return u[len(pfx):]
	}
	return u
}
