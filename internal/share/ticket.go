// Package share implements the `ccm share` HTTP reverse-proxy + Cloudflare
// tunnel feature.
//
// Flow summary:
//  1. Launch a reverse proxy on a random loopback port in CAPTURE mode.
//  2. Spawn `claude -p "just say 'hi'"` with ANTHROPIC_BASE_URL pointed at
//     the proxy. Capture its outbound identity headers (User-Agent, the
//     X-Stainless-* set, Anthropic-Version/Beta, etc.) from the first real
//     request.
//  3. Transition the proxy to SERVING mode. Serving mode forwards every
//     inbound request to api.anthropic.com after swapping the client's
//     bearer token for the real OAuth access token and rewriting the
//     captured identity headers on top of whatever the client sent.
//  4. Start a Cloudflare Quick Tunnel pointing at the local proxy.
//  5. Mint a random access token and print a ticket = base64("https://
//     <token>@<tunnel>"). The ticket is what the remote side consumes.
package share

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// Ticket is the base64-encoded "https://<token>@<host>" string printed by
// `ccm share` and consumed by the remote side.
type Ticket struct {
	Token string // random opaque bearer that the remote side must present
	Host  string // tunnel host, e.g. "foo-bar-baz.trycloudflare.com"
}

// NewRandomToken returns a 32-byte cryptographically random hex string.
func NewRandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Encode returns the base64-encoded URL form: base64("https://<token>@<host>").
func (t Ticket) Encode() string {
	raw := fmt.Sprintf("https://%s@%s", t.Token, t.Host)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// DecodeTicket parses a ticket string produced by Encode.
func DecodeTicket(s string) (Ticket, error) {
	s = strings.TrimSpace(s)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket is not valid base64: %w", err)
	}
	u, err := url.Parse(string(raw))
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket does not decode to a URL: %w", err)
	}
	if u.Scheme != "https" {
		return Ticket{}, fmt.Errorf("ticket scheme must be https (got %q)", u.Scheme)
	}
	if u.User == nil || u.User.Username() == "" {
		return Ticket{}, fmt.Errorf("ticket is missing access token")
	}
	if u.Host == "" {
		return Ticket{}, fmt.Errorf("ticket is missing host")
	}
	return Ticket{Token: u.User.Username(), Host: u.Host}, nil
}
