// Package httpx provides the single process-wide HTTP transport used by
// every online ccm command. When the CCM_PROXY environment variable is
// set to a valid proxy URL (http, https, socks5, or socks5h), every
// outbound request from Category A (reverse-proxy forwarding in
// `ccm share` / `ccm launch`) and Category B (OAuth, profile, quota
// calls in internal/oauth) is routed through that proxy.
//
// When CCM_PROXY is unset, ccm makes direct connections. Stdlib proxy
// environment variables (HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, NO_PROXY)
// are deliberately NOT consulted in either case — CCM_PROXY is the one
// and only proxy knob ccm respects. This is intentional so that a user
// who has HTTPS_PROXY exported for an unrelated tool does not end up
// with ccm silently routed through it.
//
// Online commands (login, refresh, status, backup, share, launch) must
// call Configure() from their Cobra PreRunE hook so that a malformed
// CCM_PROXY fails the command before any network I/O.
package httpx

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
)

const envVar = "CCM_PROXY"

// current holds the transport built from CCM_PROXY by Configure. Nil
// before Configure runs. Read via Transport() on the hot path.
var current atomic.Pointer[http.RoundTripper]

// fallback is the proxy-less transport used by Transport() when
// Configure has not been called. Initialized in init() so there is no
// lock on the read path.
var fallback http.RoundTripper

func init() {
	// Clone http.DefaultTransport to inherit its HTTP/2, dial
	// timeouts, idle-pool, and keep-alive settings, then explicitly
	// null out Proxy so the fallback ignores HTTPS_PROXY and friends.
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	fallback = t
}

// Configure reads CCM_PROXY from the environment, validates it, and
// caches the resulting RoundTripper. Idempotent and safe to call
// repeatedly — tests rely on this (they t.Setenv and re-Configure).
// Returns a descriptive error when CCM_PROXY is set to a value that
// is not a valid URL or uses an unsupported scheme.
func Configure() error {
	t, err := build()
	if err != nil {
		return err
	}
	current.Store(&t)
	return nil
}

// Transport returns the shared http.RoundTripper. Before Configure
// runs, returns the proxy-less fallback (see init). After Configure
// with CCM_PROXY unset, also returns a proxy-less transport — the two
// paths are behaviorally identical.
func Transport() http.RoundTripper {
	if p := current.Load(); p != nil {
		return *p
	}
	return fallback
}

// Client returns a new *http.Client using Transport(). Timeout is
// zero; callers that need a timeout should set it on the returned
// client (preserves per-call-site timeout discretion in internal/oauth).
func Client() *http.Client {
	return &http.Client{Transport: Transport()}
}

// build reads CCM_PROXY and returns the corresponding RoundTripper.
// When CCM_PROXY is unset or empty, returns a proxy-less clone of
// http.DefaultTransport.
func build() (http.RoundTripper, error) {
	raw := os.Getenv(envVar)

	base := http.DefaultTransport.(*http.Transport).Clone()

	if raw == "" {
		base.Proxy = nil
		return base, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", envVar, raw, err)
	}

	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		// supported
	case "":
		return nil, fmt.Errorf("invalid %s %q: missing scheme (expected http, https, socks5, or socks5h)", envVar, raw)
	default:
		return nil, fmt.Errorf("invalid %s %q: unsupported scheme %q (expected http, https, socks5, or socks5h)", envVar, raw, u.Scheme)
	}

	if u.Host == "" {
		return nil, fmt.Errorf("invalid %s %q: missing host", envVar, raw)
	}

	base.Proxy = http.ProxyURL(u)
	return base, nil
}
