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
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
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
//
// LocalProxy operates in two modes:
//
//   - Single-cred mode (NewLocalProxy(cred)): tokens wraps a fresh
//     *credState; pool is nil; refreshLoop runs the background OAuth
//     refresh cadence directly.
//
//   - Pool mode (NewLocalProxyWithPool(pool, debug)): tokens is the
//     pool itself (it satisfies tokenSource); pool is set so
//     ModifyResponse can signal upstream-401 events. Per-cred refresh
//     timers and the rotation scheduler that StartPoolBackground
//     wires up handle refresh cadence; LocalProxy's own refreshLoop
//     is a no-op in this mode.
type LocalProxy struct {
	listener net.Listener
	server   *http.Server
	upstream *url.URL
	rp       *httputil.ReverseProxy

	tokens tokenSource // wired by NewLocalProxy / NewLocalProxyWithPool
	pool   *credPool   // nil in single-cred mode

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
	return newLocalProxyInternal(newCredState(cred), nil, os.Getenv("CCM_LAUNCH_DEBUG") == "1")
}

// NewLocalProxyWithPool builds a load-balance LocalProxy. The
// supplied pool serves as the tokenSource; ModifyResponse signals
// upstream-401 events back into pool.SignalActivatedFailed so the
// next scheduler tick can demote/rotate as appropriate. Per-cred
// refresh and rotation are driven by StartPoolBackground; the
// proxy's own refreshLoop is a no-op when pool is non-nil.
func NewLocalProxyWithPool(pool *credPool, debug bool) (*LocalProxy, error) {
	if pool == nil {
		return nil, errors.New("NewLocalProxyWithPool: nil pool")
	}
	return newLocalProxyInternal(pool, pool, debug)
}

func newLocalProxyInternal(tokens tokenSource, pool *credPool, debug bool) (*LocalProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// coverage: unreachable — net.Listen on 127.0.0.1:0 only
		// fails when the loopback adapter is missing; not exercisable
		// in tests.
		return nil, fmt.Errorf("listen: %w", err)
	}
	upstream, err := url.Parse(upstreamBase())
	if err != nil {
		// coverage: unreachable — upstreamBase() always returns a
		// well-formed URL (constant or test override).
		ln.Close()
		return nil, fmt.Errorf("parse upstream: %w", err)
	}

	p := &LocalProxy{
		listener: ln,
		upstream: upstream,
		tokens:   tokens,
		pool:     pool,
		done:     make(chan struct{}),
		debug:    debug,
	}
	p.rp = &httputil.ReverseProxy{
		Director:     p.director,
		ErrorHandler: p.onUpstreamError,
		Transport:    httpx.Transport(),
		// See proxy.go: -1 flushes every write immediately. There is
		// no Cloudflare tunnel in front of the local proxy so the 100s
		// limit does not apply, but immediate flush still matters for
		// SSE streaming to the child claude.
		FlushInterval: -1,
	}
	// ModifyResponse is wired in all modes (single-cred and pool):
	//   - Pool-only: 401 → SignalActivatedFailed (scheduler picks up
	//     on the next tick via the preFail-aware path).
	//   - All modes: 2xx → install the usage tee so ccm stats can
	//     observe per-Claude-Code-session token consumption. Without
	//     this, the most common path (`ccm launch <id>`) records
	//     nothing.
	p.rp.ModifyResponse = func(r *http.Response) error {
		if pool != nil && r.StatusCode == http.StatusUnauthorized {
			pool.SignalActivatedFailed()
		}
		if r.StatusCode >= 200 && r.StatusCode < 300 {
			installUsageTee(r)
		}
		return nil
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

// Done returns a channel that closes when Close is called. Used by
// StartPoolBackground to tie scheduler/timer lifetimes to the proxy.
func (p *LocalProxy) Done() <-chan struct{} { return p.done }

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

// refreshLoop is the background token refresher (single-cred mode).
// Every backgroundRefreshInterval it calls getFreshToken, which is a
// no-op when the cached access token is still valid and triggers a
// full OAuth refresh otherwise. Errors are logged but not fatal —
// the next inbound request retries on its own code path and the
// child claude stays up.
//
// In pool mode this loop is a no-op: per-cred refresh timers and
// the scheduler goroutine that StartPoolBackground wires up handle
// refresh cadence for every entry.
func (p *LocalProxy) refreshLoop() {
	if p.pool != nil {
		return
	}
	ticker := time.NewTicker(backgroundRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			if _, err := p.getFreshToken(); err != nil {
				// coverage: unreachable in unit tests — the single-
				// cred refresh path requires real OAuth roundtrips.
				fmt.Fprintf(errLog(), "ccm launch: background refresh check failed: %v\n", err)
				continue
			}
			if p.debug {
				// coverage: unreachable in unit tests — debug logging
				// is environment-gated.
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
		if errors.Is(err, errNoActivated) {
			writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "ccm: no usable credentials")
			return
		}
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
//
// Note: in pool mode we deliberately do NOT replay
// pool.activatedHeaders. The spawned claude provides its own
// outbound headers per request; replaying captured headers would
// double-up identity material and risk drift between the headers and
// the bearer we're about to inject.
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

// getFreshToken returns the current access token via the configured
// tokenSource. In single-cred mode it delegates to credState.Fresh
// (peer-write reload + cross-process flock during refresh). In pool
// mode it routes through credPool.Fresh, which returns the activated
// entry's token or errNoActivated when the pool is empty.
func (p *LocalProxy) getFreshToken() (string, error) {
	if p.tokens == nil {
		// coverage: unreachable — newLocalProxyInternal always
		// initializes tokens; the nil-cred / nil-pool guards in the
		// public constructors reject the misuse before we get here.
		return "", errors.New("no credential")
	}
	return p.tokens.Fresh()
}
