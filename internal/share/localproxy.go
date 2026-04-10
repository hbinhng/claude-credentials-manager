package share

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// LocalProxy is a passthrough reverse proxy that runs on a random
// loopback port and forwards every request to api.anthropic.com with
// the Authorization header replaced by a ccm-managed credential's
// OAuth bearer. It exists so `ccm launch <account>` can run Claude
// Code against a specific credential without having to mutate
// ~/.claude/.credentials.json via `ccm use` first.
//
// Unlike share.Proxy there is no CAPTURE phase, no access token
// handshake, and no state machine. The child `claude` process is
// pointed at the proxy via ANTHROPIC_BASE_URL, and we deliberately
// leave ANTHROPIC_AUTH_TOKEN unset so claude-cli stays on its
// keychain-OAuth code path — that is what keeps the oauth-2025-04-20
// flag in Anthropic-Beta, which is required for OAuth bearers and
// which claude-cli otherwise drops when ANTHROPIC_AUTH_TOKEN is set.
//
// Because the proxy binds to 127.0.0.1 only, any local process on
// the machine can use it for the duration of its lifetime. Callers
// are expected to start it, launch a single child `claude`, and
// shut it down when `claude` exits — same trust model as
// ~/.claude/.credentials.json itself.
type LocalProxy struct {
	listener net.Listener
	server   *http.Server
	upstream *url.URL
	rp       *httputil.ReverseProxy

	credMu sync.Mutex
	cred   *store.Credential

	debug bool

	// done is closed by Close() to signal the background token refresher
	// started by Start() to exit. doneOnce guards the close so Close can
	// be called more than once without panicking.
	done     chan struct{}
	doneOnce sync.Once
}

// NewLocalProxy builds a local passthrough proxy for the given
// credential. It listens on a random loopback port; call Start to run
// the HTTP server loop and Close to stop it.
func NewLocalProxy(cred *store.Credential) (*LocalProxy, error) {
	if cred == nil {
		return nil, errors.New("NewLocalProxy: nil credential")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	upstream, err := url.Parse(upstreamBase)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("parse upstream: %w", err)
	}

	p := &LocalProxy{
		listener: ln,
		upstream: upstream,
		cred:     cred,
		done:     make(chan struct{}),
		debug:    os.Getenv("CCM_LAUNCH_DEBUG") == "1",
	}
	p.rp = &httputil.ReverseProxy{
		Director:     p.director,
		ErrorHandler: p.onUpstreamError,
		Transport:    http.DefaultTransport,
		// See proxy.go: -1 flushes every write immediately. There is
		// no Cloudflare tunnel in front of the local proxy so the 100s
		// limit does not apply, but immediate flush still matters for
		// SSE streaming to the child claude.
		FlushInterval: -1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handle)
	p.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	return p, nil
}

// Addr returns the loopback URL the proxy is listening on,
// e.g. "http://127.0.0.1:45123".
func (p *LocalProxy) Addr() string {
	return "http://" + p.listener.Addr().String()
}

// Start runs the HTTP server. It blocks until the server exits; run
// it in its own goroutine. Start also kicks off a background token
// refresher that exits when Close is called.
func (p *LocalProxy) Start() error {
	go p.refreshLoop()
	if err := p.server.Serve(p.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close gracefully stops the proxy and signals the background token
// refresher started by Start to exit.
func (p *LocalProxy) Close() error {
	p.doneOnce.Do(func() { close(p.done) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

// refreshLoop is the background token refresher. Every
// backgroundRefreshInterval it calls getFreshToken, which is a no-op
// when the cached access token is still valid and triggers a full
// OAuth refresh otherwise. Errors are logged but not fatal — the
// next inbound request retries on its own code path and the child
// claude stays up.
func (p *LocalProxy) refreshLoop() {
	ticker := time.NewTicker(backgroundRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			if _, err := p.getFreshToken(); err != nil {
				fmt.Fprintf(errLog(), "ccm launch: background refresh check failed: %v\n", err)
				continue
			}
			if p.debug {
				fmt.Fprintf(errLog(), "ccm launch [debug]: background refresh check ok\n")
			}
		}
	}
}

// handle forwards any inbound request to api.anthropic.com with a
// freshly minted OAuth bearer.
func (p *LocalProxy) handle(w http.ResponseWriter, r *http.Request) {
	realToken, err := p.getFreshToken()
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "ccm launch: credential refresh failed: "+err.Error())
		return
	}
	ctx := context.WithValue(r.Context(), ctxKeyRealToken, realToken)
	p.rp.ServeHTTP(w, r.WithContext(ctx))
}

// director rewrites the outbound request: set upstream URL and inject
// the real OAuth bearer. Unlike Proxy.director we do not strip or
// overlay any other headers — claude-cli, running in its normal
// keychain-OAuth code path, already produces the exact identity
// headers Anthropic expects. We only need to replace the bearer.
func (p *LocalProxy) director(req *http.Request) {
	req.URL.Scheme = p.upstream.Scheme
	req.URL.Host = p.upstream.Host
	req.Host = p.upstream.Host

	// Replace whatever Authorization claude-cli sent (from its
	// keychain) with our target credential's real OAuth bearer.
	req.Header.Del("Authorization")
	if token, ok := req.Context().Value(ctxKeyRealToken).(string); ok {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Belt & braces: scrub any API-key header in case the env got
	// sideloaded from somewhere we didn't catch.
	req.Header.Del("X-Api-Key")

	if p.debug {
		fmt.Fprintf(errLog(), "ccm launch [debug]: forwarding %s %s\n", req.Method, req.URL.String())
		for _, h := range []string{"Authorization", "Anthropic-Beta", "Anthropic-Version", "User-Agent", "X-Claude-Code-Session-Id"} {
			if v := req.Header.Get(h); v != "" {
				if h == "Authorization" {
					fmt.Fprintf(errLog(), "  %s: Bearer <%d chars>\n", h, len(v)-len("Bearer "))
				} else {
					fmt.Fprintf(errLog(), "  %s: %s\n", h, v)
				}
			}
		}
	}
}

// onUpstreamError surfaces a structured error to the child claude
// when the upstream request fails for transport reasons.
func (p *LocalProxy) onUpstreamError(w http.ResponseWriter, _ *http.Request, err error) {
	writeAnthropicError(w, http.StatusBadGateway, "api_error", "ccm launch: upstream error: "+err.Error())
}

// getFreshToken returns the current access token, refreshing it first
// if it is expired or expiring soon. Refresh is synchronized on
// credMu. Mirrors Proxy.getFreshToken — the two proxies intentionally
// do not share state, so duplicating this small helper is cheaper
// than wiring up a shared abstraction.
func (p *LocalProxy) getFreshToken() (string, error) {
	p.credMu.Lock()
	defer p.credMu.Unlock()
	if p.cred == nil {
		return "", errors.New("no credential")
	}
	if p.cred.IsExpired() || p.cred.IsExpiringSoon() {
		tokens, err := oauth.Refresh(p.cred.ClaudeAiOauth.RefreshToken)
		if err != nil {
			return "", fmt.Errorf("refresh: %w", err)
		}
		p.cred.ClaudeAiOauth.AccessToken = tokens.AccessToken
		if tokens.RefreshToken != "" {
			p.cred.ClaudeAiOauth.RefreshToken = tokens.RefreshToken
		}
		p.cred.ClaudeAiOauth.ExpiresAt = time.Now().UnixMilli() + tokens.ExpiresIn*1000
		if scopes := strings.Fields(tokens.Scope); len(scopes) > 0 {
			p.cred.ClaudeAiOauth.Scopes = scopes
		}
		p.cred.LastRefreshedAt = time.Now().UTC().Format(time.RFC3339)
		if err := store.Save(p.cred); err != nil {
			fmt.Fprintf(errLog(), "ccm launch: warning: failed to persist refreshed credential: %v\n", err)
		}
	}
	return p.cred.ClaudeAiOauth.AccessToken, nil
}
