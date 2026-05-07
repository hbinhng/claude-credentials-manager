package cmd

import (
	"fmt"
	"net/url"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(ticketCmd)
	ticketCmd.Flags().String("from-endpoint", "", "full URL of the share endpoint, e.g. https://abc.trycloudflare.com")
	ticketCmd.Flags().String("from-access-token", "", "access token to embed in the ticket")
	_ = ticketCmd.MarkFlagRequired("from-endpoint")
	_ = ticketCmd.MarkFlagRequired("from-access-token")
}

var ticketCmd = &cobra.Command{
	Use:   "ticket",
	Short: "Build a ccm launch ticket from an endpoint URL and access token",
	Long: `Build the base64 ticket consumed by ` + "`ccm launch --via`" + ` from a
share endpoint and access token supplied on the command line. Offline:
no credential store, no network, no proxy. Useful when you already have
a tunnel and bearer in hand and just need the encoded form.

Example:

  ccm ticket --from-endpoint https://abc.trycloudflare.com \
             --from-access-token <token>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		endpoint, _ := cmd.Flags().GetString("from-endpoint")
		token, _ := cmd.Flags().GetString("from-access-token")
		out, err := buildTicket(endpoint, token)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), out)
		return nil
	},
}

// buildTicket validates the endpoint URL and access token, then returns
// the base64-encoded ticket. Validation rules: endpoint must be a full
// http:// or https:// URL with host only (no path/query/fragment/userinfo);
// token must consist of RFC 3986 unreserved bytes only.
func buildTicket(endpoint, token string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("--from-endpoint is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("--from-endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("--from-endpoint: scheme must be http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("--from-endpoint: missing host")
	}
	if u.Path != "" {
		return "", fmt.Errorf("--from-endpoint: path not allowed (got %q)", u.Path)
	}
	if u.RawQuery != "" {
		return "", fmt.Errorf("--from-endpoint: query not allowed")
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("--from-endpoint: fragment not allowed")
	}
	if u.User != nil {
		return "", fmt.Errorf("--from-endpoint: userinfo not allowed")
	}

	if token == "" {
		return "", fmt.Errorf("--from-access-token is required")
	}
	for i := 0; i < len(token); i++ {
		if !isUnreserved(token[i]) {
			return "", fmt.Errorf("--from-access-token: byte %d is %q, only A-Z a-z 0-9 - . _ ~ allowed", i, string(token[i]))
		}
	}

	return share.EncodeTicket(share.Ticket{
		Scheme: u.Scheme,
		Host:   u.Host,
		Token:  token,
	}), nil
}

// isUnreserved reports whether b is in the RFC 3986 unreserved set.
func isUnreserved(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z':
		return true
	case b >= 'a' && b <= 'z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '-' || b == '.' || b == '_' || b == '~':
		return true
	}
	return false
}
