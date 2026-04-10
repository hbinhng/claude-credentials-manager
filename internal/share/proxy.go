package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// identityHeaderAllowlist enumerates the request headers that identify a
// Claude Code install. We record these in CAPTURE mode and replay them on
// every forwarded request in SERVING mode so that upstream sees a
// consistent identity no matter which machine the inbound request came
// from. Keys are canonical MIME header form (net/http canonicalizes for
// us).
var identityHeaderAllowlist = []string{
	"User-Agent",
	"X-App",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-Os",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"Anthropic-Version",
	"Anthropic-Beta",
	"Anthropic-Dangerous-Direct-Browser-Access",
	"Accept",
	"Accept-Encoding",
}

// clientDenylist are headers we strip from the inbound request before
// forwarding. httputil.ReverseProxy already removes hop-by-hop headers per
// RFC 7230; this list is for things we specifically don't want to forward.
var clientDenylist = []string{
	"Authorization",     // replaced with real OAuth bearer
	"X-Api-Key",         // no client-provided API key
	"Proxy-Authorization",
	"Cookie",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
}

// Proxy modes.
const (
	modeCapturing int32 = iota
	modeServing
)

// backgroundRefreshInterval is how often the serving-mode refresher wakes
// up to check the credential's expiration and refresh if needed. Anthropic
// OAuth tokens are long-lived (hours), so this cadence is generous — the
// call is a no-op when the token is still valid. The point is to keep the
// token warm during long idle periods so the first request after a quiet
// stretch does not eat a full OAuth round-trip, and to close any window
// where the token expires between requests.
const backgroundRefreshInterval = 30 * time.Minute

// Proxy is the HTTP reverse proxy that powers `ccm share`.
//
// It starts in CAPTURE mode: any inbound request records its identity
// headers (see identityHeaderAllowlist) and returns a synthetic error
// response. Once the first request has been captured, the capture channel
// is closed and the caller is expected to call Transition() to switch the
// proxy into SERVING mode.
//
// In SERVING mode, the proxy validates that the inbound Authorization
// bearer matches the access token minted for this share session, strips
// the client's identity headers, replays the captured ones, injects the
// real OAuth bearer, and streams the request to api.anthropic.com via
// net/http/httputil.ReverseProxy.
type Proxy struct {
	listener net.Listener
	server   *http.Server
	upstream *url.URL
	rp       *httputil.ReverseProxy

	accessToken string // bearer that inbound requests must present in SERVING mode

	// State transition
	modeMu   sync.RWMutex
	mode     int32
	captured http.Header
	captureC chan struct{} // closed once capture has happened

	// Credential state (may be mutated when refreshing).
	credMu sync.Mutex
	cred   *store.Credential

	// debug enables verbose per-request logging in the Director. Toggled
	// via CCM_SHARE_DEBUG=1 at NewProxy time.
	debug bool

	// done is closed by Close() to signal background goroutines (currently
	// only the serving-mode token refresher started by Transition) to
	// exit. doneOnce guards the close so Close can be called more than
	// once without panicking.
	done     chan struct{}
	doneOnce sync.Once
}

// ctxKey is used to thread per-request state from the outer handler into
// the ReverseProxy Director callback.
type ctxKey int

const (
	ctxKeyRealToken ctxKey = iota
)

const upstreamBase = "https://api.anthropic.com"

// NewProxy builds a proxy listening on a random loopback port. The proxy
// starts in CAPTURE mode; call Transition() once capture is complete.
func NewProxy() (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	upstream, err := url.Parse(upstreamBase)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("parse upstream: %w", err)
	}

	p := &Proxy{
		listener: ln,
		upstream: upstream,
		mode:     modeCapturing,
		captureC: make(chan struct{}),
		done:     make(chan struct{}),
		debug:    os.Getenv("CCM_SHARE_DEBUG") == "1",
	}
	p.rp = &httputil.ReverseProxy{
		Director:     p.director,
		ErrorHandler: p.onUpstreamError,
		// Use a transport that handles HTTP/2 and streaming responses
		// (SSE used by /v1/messages with stream=true). http.DefaultTransport
		// is sufficient here; Go's client-side auto-negotiates h2 via ALPN.
		Transport: httpx.Transport(),
		// FlushInterval = -1 flushes every write immediately. This matters
		// because Cloudflare Quick Tunnels enforce a 100s "origin response"
		// timeout — if no bytes (including response headers) reach the edge
		// within 100s of the request, the tunnel returns 524 to the remote
		// client. The Go stdlib ReverseProxy's copyResponse arms an initial
		// flush timer with this interval, so -1 pushes response headers to
		// the wire the instant RoundTrip returns, without waiting for a
		// body byte or a batching window. Every subsequent body write also
		// flushes immediately, which is what we want for SSE anyway (Go
		// auto-overrides to -1 for text/event-stream, but we just make it
		// the uniform behavior for every response).
		FlushInterval: -1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ccm-share/healthz", p.handleHealth)
	mux.HandleFunc("/", p.handle)
	p.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	return p, nil
}

// Addr returns the loopback URL the proxy is listening on,
// e.g. "http://127.0.0.1:45123".
func (p *Proxy) Addr() string {
	return "http://" + p.listener.Addr().String()
}

// Port returns the TCP port the proxy bound to.
func (p *Proxy) Port() int {
	return p.listener.Addr().(*net.TCPAddr).Port
}

// Start runs the HTTP server. It blocks until the server exits; run it in
// its own goroutine.
func (p *Proxy) Start() error {
	if err := p.server.Serve(p.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close gracefully stops the proxy and signals any background goroutines
// started by Transition (the serving-mode token refresher) to exit.
func (p *Proxy) Close() error {
	p.doneOnce.Do(func() { close(p.done) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

// CaptureDone returns a channel that is closed once identity headers have
// been captured from the first inbound request.
func (p *Proxy) CaptureDone() <-chan struct{} {
	return p.captureC
}

// Captured returns the captured identity headers. Returns nil until the
// first capture has happened.
func (p *Proxy) Captured() http.Header {
	p.modeMu.RLock()
	defer p.modeMu.RUnlock()
	return p.captured.Clone()
}

// Transition switches the proxy from CAPTURE mode into SERVING mode.
// accessToken is the random bearer that inbound clients must present.
// cred is the ccm-managed credential whose OAuth access token will be
// injected into forwarded requests.
//
// Returns an error if capture never happened. On success, starts a
// background goroutine that periodically refreshes the credential so it
// does not die mid-session during long idle stretches. The goroutine
// exits when Close is called.
func (p *Proxy) Transition(accessToken string, cred *store.Credential) error {
	p.modeMu.Lock()
	if p.captured == nil {
		p.modeMu.Unlock()
		return errors.New("cannot transition: capture never happened")
	}
	p.accessToken = accessToken
	p.cred = cred
	p.mode = modeServing
	p.modeMu.Unlock()

	go p.refreshLoop()
	return nil
}

// refreshLoop is the serving-mode background token refresher. Every
// backgroundRefreshInterval it calls getFreshToken, which is a no-op
// when the cached access token is still valid and triggers a full
// OAuth refresh otherwise. Errors are logged but not fatal — the
// next inbound request will retry refresh on its own code path, and
// the share session stays up.
func (p *Proxy) refreshLoop() {
	ticker := time.NewTicker(backgroundRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			if _, err := p.getFreshToken(); err != nil {
				fmt.Fprintf(errLog(), "ccm share: background refresh check failed: %v\n", err)
				continue
			}
			if p.debug {
				fmt.Fprintf(errLog(), "ccm share [debug]: background refresh check ok\n")
			}
		}
	}
}

// handleHealth is a lightweight readiness endpoint reachable in both
// capture and serving modes. `ccm share` polls this through the tunnel
// after it comes up so it only reports "live" once the Cloudflare edge
// has actually propagated the route (avoiding the classic error 1033
// window immediately after the tunnel URL is printed).
func (p *Proxy) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handle is the top-level HTTP handler. It dispatches on the current mode.
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	p.modeMu.RLock()
	mode := p.mode
	p.modeMu.RUnlock()

	switch mode {
	case modeCapturing:
		p.handleCapture(w, r)
	case modeServing:
		p.handleServe(w, r)
	default:
		http.Error(w, "proxy in unknown state", http.StatusInternalServerError)
	}
}

// handleCapture extracts identity headers from the inbound request and
// returns a synthetic error response so `claude -p` exits quickly.
//
// Only POST requests to /v1/messages (the real API call) are used for
// capture. claude-cli opens with a HEAD / liveness probe whose User-Agent
// is "Bun/1.3.11" and which carries none of the Anthropic-* identity
// headers; capturing that probe would leave the serving-mode replay with
// only a bogus User-Agent, which in turn lets the client's (stripped
// down) Anthropic-Beta leak upstream and causes a 401 from Anthropic.
func (p *Proxy) handleCapture(w http.ResponseWriter, r *http.Request) {
	// Drain the body so the client sees a clean close.
	_, _ = io.Copy(io.Discard, r.Body)

	// Ignore probes and everything that isn't the actual /v1/messages
	// POST. Return a benign 200 so the liveness check passes and claude
	// proceeds to the real POST.
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/messages") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
		return
	}

	captured := http.Header{}
	for _, h := range identityHeaderAllowlist {
		if v := r.Header.Values(h); len(v) > 0 {
			captured[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
		}
	}

	// Only record on the first capture; subsequent retries from the same
	// claude process are ignored but still handled so the client gets a
	// clean response.
	p.modeMu.Lock()
	first := p.captured == nil
	if first {
		p.captured = captured
	}
	p.modeMu.Unlock()

	if first {
		if p.debug {
			fmt.Fprintf(errLog(), "ccm share [debug]: captured %d identity headers from %s %s\n", len(captured), r.Method, r.URL.Path)
			for k, v := range captured {
				fmt.Fprintf(errLog(), "  %s: %s\n", k, strings.Join(v, ", "))
			}
		}
		close(p.captureC)
	}

	// 401 with an Anthropic-shaped error body. Claude-cli treats auth
	// errors as terminal and does not retry indefinitely.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"ccm share: capture complete"}}`))
}

// handleServe validates the inbound bearer, refreshes the credential if
// needed, and forwards via the ReverseProxy.
func (p *Proxy) handleServe(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	expected := "Bearer " + p.accessToken
	if auth != expected {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing access token")
		return
	}

	realToken, err := p.getFreshToken()
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "ccm share: credential refresh failed: "+err.Error())
		return
	}

	ctx := context.WithValue(r.Context(), ctxKeyRealToken, realToken)
	p.rp.ServeHTTP(w, r.WithContext(ctx))
}

// director rewrites the outbound request: set upstream URL, strip client
// headers, replay captured identity headers, inject real OAuth bearer.
func (p *Proxy) director(req *http.Request) {
	req.URL.Scheme = p.upstream.Scheme
	req.URL.Host = p.upstream.Host
	req.Host = p.upstream.Host

	// Strip client-side headers we don't want forwarded.
	for _, h := range clientDenylist {
		req.Header.Del(h)
	}

	// Overlay captured identity headers on top of whatever the client sent.
	// We take a snapshot under the read lock to avoid racing with
	// Transition (which only writes once, but cheap anyway).
	p.modeMu.RLock()
	captured := p.captured
	p.modeMu.RUnlock()
	for k, vs := range captured {
		req.Header[k] = append([]string(nil), vs...)
	}

	// Inject the real OAuth bearer (thread-through from handleServe).
	if token, ok := req.Context().Value(ctxKeyRealToken).(string); ok {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	if p.debug {
		fmt.Fprintf(errLog(), "ccm share [debug]: forwarding %s %s\n", req.Method, req.URL.String())
		for _, h := range []string{"Authorization", "Anthropic-Beta", "Anthropic-Version", "User-Agent", "X-Claude-Code-Session-Id"} {
			if v := req.Header.Get(h); v != "" {
				if h == "Authorization" {
					fmt.Fprintf(errLog(), "  %s: Bearer <%d chars>\n", h, len(v)-len("Bearer "))
				} else {
					fmt.Fprintf(errLog(), "  %s: %s\n", h, v)
				}
			} else {
				fmt.Fprintf(errLog(), "  %s: <unset>\n", h)
			}
		}
	}
}

// onUpstreamError surfaces a structured error to the client when the
// upstream request fails for transport reasons (DNS, dial, EOF, ...).
func (p *Proxy) onUpstreamError(w http.ResponseWriter, _ *http.Request, err error) {
	writeAnthropicError(w, http.StatusBadGateway, "api_error", "ccm share: upstream error: "+err.Error())
}

// getFreshToken returns the current access token, refreshing it first if
// it is expired or expiring soon. Refresh is synchronized on credMu.
func (p *Proxy) getFreshToken() (string, error) {
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
			// Non-fatal: we still have a valid in-memory token, but warn.
			fmt.Fprintf(errLog(), "ccm share: warning: failed to persist refreshed credential: %v\n", err)
		}
	}
	return p.cred.ClaudeAiOauth.AccessToken, nil
}

// writeAnthropicError emits a JSON body in Anthropic's error envelope
// shape so clients can parse it with their normal error handling.
func writeAnthropicError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	payload := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    kind,
			"message": message,
		},
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// errLog returns a writer for diagnostic log output. Abstracted so tests
// can redirect it if needed.
var errLog = func() io.Writer { return os.Stderr }
