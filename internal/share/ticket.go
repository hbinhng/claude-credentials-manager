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
	"fmt"
	"net/url"
	"strings"
)

// Ticket is the base64-encoded "<scheme>://<token>@<host>" string printed
// by `ccm share` and consumed by the remote side. Scheme is "https" for
// the Cloudflare Quick Tunnel path and "http" for the `--bind-host` path
// where there is no tunnel.
type Ticket struct {
	Scheme string // "https" (tunnel) or "http" (bind-host)
	Token  string // random opaque bearer that the remote side must present
	Host   string // host[:port] the remote side should connect to
}

// NewRandomToken returns a 16-byte cryptographically random value encoded
// as unpadded base64url — 22 characters, 128 bits of entropy. The alphabet
// is a strict subset of URL "unreserved" characters, so the token slots
// into userinfo with zero percent-encoding.
func NewRandomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Encode returns the base64-encoded URL form:
// base64("<scheme>://<token>@<host>"). The outer envelope stays on
// base64.StdEncoding so older `ccm launch --via` clients (which do not
// know about base64url) can still decode tickets produced by newer
// `ccm share` hosts.
func (t Ticket) Encode() string {
	raw := fmt.Sprintf("%s://%s@%s", t.Scheme, t.Token, t.Host)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// EncodeTicket is a package-level convenience wrapper around Ticket.Encode.
// It exists so session.go can call EncodeTicket(t) without a method receiver,
// matching the call-site style used in the session orchestration.
func EncodeTicket(t Ticket) string {
	return t.Encode()
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
	if u.Scheme != "https" && u.Scheme != "http" {
		return Ticket{}, fmt.Errorf("ticket scheme must be http or https (got %q)", u.Scheme)
	}
	if u.User == nil || u.User.Username() == "" {
		return Ticket{}, fmt.Errorf("ticket is missing access token")
	}
	if u.Host == "" {
		return Ticket{}, fmt.Errorf("ticket is missing host")
	}
	return Ticket{Scheme: u.Scheme, Token: u.User.Username(), Host: u.Host}, nil
}
