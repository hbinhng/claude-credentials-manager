package share

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// ErrInvalidPinnedToken is returned (wrapped) by StartSession and
// ValidatePinnedToken when Options.PinnedAccessToken is non-empty
// but does not match the URL-safe charset. Callers (CLI, future
// serve UI) can errors.Is to detect and re-wrap with their own
// surface name (env var, form field, etc.).
var ErrInvalidPinnedToken = errors.New("pinned access token must match [A-Za-z0-9_-]+")

var pinnedTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidatePinnedToken returns nil if s is empty (pinning disabled)
// or matches the URL-safe charset. Otherwise the error wraps
// ErrInvalidPinnedToken.
func ValidatePinnedToken(s string) error {
	if s == "" {
		return nil
	}
	if !pinnedTokenPattern.MatchString(s) {
		return fmt.Errorf("%w (got %d chars)", ErrInvalidPinnedToken, len(s))
	}
	return nil
}

// Options controls how StartSession brings up a share session.
//
// BindHost:
//   - empty string selects tunnel mode: the proxy binds to a random
//     loopback port and a Cloudflare Quick Tunnel is started in front
//     of it.
//   - non-empty selects LAN-bind mode: the proxy listener wildcards
//     (0.0.0.0) so any host on the local network can reach it, and
//     BindHost is placed in the ticket as the address the remote side
//     should dial. No tunnel is started.
//
// BindPort:
//   - 0 lets the OS pick a port.
//   - non-zero pins the listener to that port.
//
// CapturePrompt is the prompt text forwarded to RunCapture during the
// one-shot `claude -p` identity-capture phase. When empty, StartSession
// falls back to DefaultCapturePrompt so callers that do not care about
// the prompt value do not need to supply one.
//
// Debug mirrors CCM_SHARE_DEBUG=1 and enables the per-request director
// logging that already lives in proxy.go.
type Options struct {
	BindHost      string
	BindPort      int
	CapturePrompt string // if empty, runCapture falls back to DefaultCapturePrompt
	Debug         bool

	// Load-balance mode (all-or-nothing): set by cmd/share.go when
	// --load-balance is passed. Pool owns the per-credential state;
	// RebalanceInterval is the scheduler tick. Clock is an optional
	// test seam — production wiring leaves it nil and the session
	// substitutes realClock{}.
	Pool              *credPool
	RebalanceInterval time.Duration
	Clock             clock

	// PinnedAccessToken, when non-empty, overrides the random bearer
	// that StartSession would otherwise mint. Must match
	// [A-Za-z0-9_-]+; any non-empty value outside that set yields a
	// validation error wrapping ErrInvalidPinnedToken before any
	// proxy / tunnel / scheduler is started.
	//
	// Empty (zero value) preserves today's behavior: a fresh
	// crypto/rand-derived token is minted per session.
	PinnedAccessToken string
}

// SessionStarter abstracts StartSession for tests and for consumers
// that want to inject a fake (notably internal/serve.Manager).
type SessionStarter interface {
	StartSession(cred *store.Credential, opts Options) (Session, error)
}

// Session is the handle returned by StartSession. It is the ONLY
// surface through which ccm share CLI and ccm serve's manager
// interact with a running share session — proxy, tunnel, capture,
// and ticket plumbing are all internal to the implementation.
//
// Session is safe for concurrent use.
type Session interface {
	Ticket() string       // base64-encoded ticket envelope
	CredID() string
	Mode() string // "tunnel" | "lan"
	StartedAt() time.Time
	Reach() string         // tunnel URL or "http://<bind-host>:<port>"
	Stop() error           // idempotent
	Done() <-chan struct{} // closed once Stop finishes
	Err() error            // non-nil if the session failed after Start
	Pool() PoolReader      // nil in single-cred mode
}

// defaultStarter is the production SessionStarter. StartSession is the
// package-level convenience that delegates to it.
type defaultStarter struct{}

// DefaultStarter is what ccm share CLI and ccm serve wire up in
// production. Tests construct their own SessionStarter.
var DefaultStarter SessionStarter = &defaultStarter{}

// StartSession is a convenience wrapper over DefaultStarter.StartSession.
func StartSession(cred *store.Credential, opts Options) (Session, error) {
	return DefaultStarter.StartSession(cred, opts)
}

// captureFn runs the one-shot `claude -p` capture phase against the
// given proxy using the supplied prompt. Overridable in tests so the
// suite does not require claude on PATH. Because this is a
// package-level var, tests that override it must not run in parallel
// with other tests that also touch captureFn — stash and restore the
// original in a defer.
var captureFn = runCapture

// startCloudflaredFn starts a Cloudflare Quick Tunnel in front of the
// given loopback URL and blocks until WaitReady succeeds. Returns the
// running tunnel, its public URL, and any startup error. Overridable
// in tests so the suite does not require cloudflared on PATH. Same
// parallel-safety caveat as captureFn — stash and restore.
var startCloudflaredFn = startCloudflared

type sessionImpl struct {
	credID    string
	mode      string
	reach     string
	ticket    string
	startedAt time.Time

	proxy    *Proxy
	tunnel   *Tunnel
	pool     *credPool
	stopOnce sync.Once
	done     chan struct{}
}

func (s *sessionImpl) CredID() string        { return s.credID }
func (s *sessionImpl) Mode() string          { return s.mode }
func (s *sessionImpl) Reach() string         { return s.reach }
func (s *sessionImpl) Ticket() string        { return s.ticket }
func (s *sessionImpl) StartedAt() time.Time  { return s.startedAt }
func (s *sessionImpl) Done() <-chan struct{} { return s.done }
func (s *sessionImpl) Pool() PoolReader {
	if s.pool == nil {
		return nil
	}
	return s.pool
}

// Err always returns nil in the current implementation — StartSession
// fails synchronously and there is no post-start failure-monitoring
// goroutine yet. The method exists to satisfy the Session interface
// and to leave the door open for future failure propagation without
// a signature change.
func (s *sessionImpl) Err() error { return nil }

func (s *sessionImpl) Stop() error {
	var rerr error
	s.stopOnce.Do(func() {
		if s.tunnel != nil {
			if err := s.tunnel.Close(); err != nil {
				// coverage: unreachable — NewTunnelForTest always returns nil
				// from Close; the real cloudflared path is not exercised in
				// unit tests. Error surface kept for completeness.
				rerr = err
			}
		}
		if s.proxy != nil {
			if err := s.proxy.Close(); err != nil && rerr == nil {
				// coverage: unreachable — server.Shutdown on a just-started
				// test proxy always returns nil within the 5s deadline.
				// Propagated for completeness.
				rerr = err
			}
		}
		close(s.done)
	})
	return rerr
}

func (*defaultStarter) StartSession(cred *store.Credential, opts Options) (Session, error) {
	if err := ValidatePinnedToken(opts.PinnedAccessToken); err != nil {
		return nil, err
	}

	bindAddr := ListenerBindAddr(opts.BindHost, opts.BindPort)
	proxy, err := NewProxy(bindAddr)
	if err != nil {
		return nil, fmt.Errorf("new proxy: %w", err)
	}

	proxyErrC := make(chan error, 1)
	go func() { proxyErrC <- proxy.Start() }()

	prompt := opts.CapturePrompt
	if prompt == "" {
		prompt = DefaultCapturePrompt
	}

	if opts.Pool != nil {
		// Load-balance mode: capture has already happened inside
		// BuildPool (per-cred). The main proxy never enters CAPTURE
		// state; seed an empty p.captured so Transition's
		// capture-required gate passes. Director branches on
		// p.pool != nil and reads headers from the pool, never from
		// p.captured.
		proxy.markCaptured(http.Header{})
	} else {
		// Single-cred mode: existing behavior.
		if err := captureFn(proxy, prompt); err != nil {
			_ = proxy.Close()
			return nil, fmt.Errorf("capture: %w", err)
		}
	}

	var accessToken string
	if opts.PinnedAccessToken != "" {
		accessToken = opts.PinnedAccessToken
		fmt.Fprintln(errLog(), "ccm share: using pinned access token")
	} else {
		var err error
		accessToken, err = newAccessToken()
		if err != nil {
			_ = proxy.Close()
			// coverage: unreachable — crypto/rand only errors on a kernel RNG
			// failure, which is not exercisable in tests.
			return nil, err
		}
	}

	// Pick tokenSource: pool when --load-balance, else single-cred
	// credState.
	var tokens tokenSource
	if opts.Pool != nil {
		tokens = opts.Pool
	} else {
		tokens = newCredState(cred)
	}
	if err := proxy.Transition(accessToken, tokens, opts.Pool); err != nil {
		_ = proxy.Close()
		// coverage: unreachable — Transition errors only when capture has
		// not run; StartSession always runs capture first via captureFn.
		return nil, fmt.Errorf("transition: %w", err)
	}

	// Pool mode: start scheduler + per-cred refresh timers.
	if opts.Pool != nil {
		c := opts.Clock
		if c == nil {
			c = realClock{}
		}
		// Pre-flight: if Fresh fails synchronously, run one tick to
		// see if rotation can pick a replacement.
		if _, ferr := opts.Pool.Fresh(); ferr != nil {
			sch := newScheduler(opts.Pool, productionProbe, c, opts.RebalanceInterval)
			sch.runOnce()
			if _, ferr2 := opts.Pool.Fresh(); ferr2 != nil {
				_ = proxy.Close()
				return nil, fmt.Errorf("pool transition: no usable credential after pre-flight rotation: %w", ferr2)
			}
		}
		for _, e := range opts.Pool.entries {
			state := e.state
			go runRefreshTimer(state, c, jitterFn, proxy.done)
		}
		sch := newScheduler(opts.Pool, productionProbe, c, opts.RebalanceInterval)
		sch.SetDebug(opts.Debug)
		sch.prompt = opts.CapturePrompt // empty OK — captureFn defaults to DefaultCapturePrompt
		go sch.Run(proxy.done)
	}

	mode := "tunnel"
	reach := ""
	var tun *Tunnel
	if opts.BindHost == "" {
		t, publicURL, cancel, err := startCloudflaredWithRetry(proxy.Addr(), opts.Debug)
		if err != nil {
			_ = proxy.Close()
			return nil, fmt.Errorf("cloudflared: %w", err)
		}
		tun = t
		reach = publicURL
		t.setShutdownHook(cancel)
	} else {
		mode = "lan"
		reach = fmt.Sprintf("http://%s:%d", opts.BindHost, proxy.Port())
	}

	ticket := EncodeTicket(Ticket{
		Scheme: schemeForMode(mode),
		Host:   hostForMode(mode, opts, proxy, reach),
		Token:  accessToken,
	})

	return &sessionImpl{
		credID:    cred.ID,
		mode:      mode,
		reach:     reach,
		ticket:    ticket,
		startedAt: time.Now(),
		proxy:     proxy,
		tunnel:    tun,
		pool:      opts.Pool,
		done:      make(chan struct{}),
	}, nil
}

func newAccessToken() (string, error) {
	return NewRandomToken()
}

func schemeForMode(m string) string {
	if m == "lan" {
		return "http"
	}
	return "https"
}

func hostForMode(m string, opts Options, p *Proxy, reach string) string {
	if m == "lan" {
		return fmt.Sprintf("%s:%d", opts.BindHost, p.Port())
	}
	return strings.TrimPrefix(reach, "https://")
}

// runCapture spawns `claude -p` against the proxy in CAPTURE mode and
// waits for the proxy to record identity headers. Delegates to the
// package-level RunCapture with a background context and the caller-
// supplied prompt.
//
// coverage: unreachable — always overridden by captureFn in tests.
// Wraps a real subprocess spawn that requires `claude` on PATH; real
// behaviour is exercised by manual smoke tests and by cmd/share at
// runtime.
func runCapture(p *Proxy, prompt string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return RunCapture(ctx, p, prompt)
}

// startCloudflared starts a Cloudflare Quick Tunnel in front of
// localURL and blocks until WaitReady succeeds.
//
// coverage: unreachable — always overridden by startCloudflaredFn in
// tests. Wraps real subprocess + network I/O; real behaviour is
// exercised by manual smoke tests and by cmd/share at runtime.
func startCloudflared(ctx context.Context, localURL string) (*Tunnel, string, error) {
	tun, err := StartTunnel(ctx, localURL)
	if err != nil {
		return nil, "", err
	}
	if err := tun.WaitReady(ctx, 60*time.Second); err != nil {
		_ = tun.Close()
		return nil, "", err
	}
	return tun, tun.PublicURL(), nil
}

// cloudflaredMaxAttempts and cloudflaredRetryBackoff control the
// retry loop in startCloudflaredWithRetry. Cloudflare Quick Tunnels
// are flaky in practice (transient DNS, edge propagation hiccups,
// QUIC/HTTP2 negotiation drops), so a single failure is rarely
// terminal — retrying buys us a much higher success rate at the
// cost of up to ~31s in the worst case.
//
// Exposed as package-level vars so tests can shorten them.
var (
	cloudflaredMaxAttempts   = 5
	cloudflaredRetryBackoff  = func(attempt int) time.Duration {
		// 1s, 2s, 4s, 8s between attempts (sum ~15s + per-attempt
		// startup latency). Capped at 16s for sanity.
		d := time.Second << uint(attempt-1)
		if d > 16*time.Second {
			d = 16 * time.Second
		}
		return d
	}
)

// startCloudflaredWithRetry wraps startCloudflaredFn in a bounded
// retry loop. Each attempt gets its own context so a cancellation
// from a previous attempt's setShutdownHook does not poison the
// retry. Returns the cancel func of the LAST successful attempt so
// the caller can wire it as the tunnel's shutdown hook.
//
// On success: returns the tunnel, its public URL, and the cancel
// func for the surviving attempt's context.
// On failure (all attempts exhausted): returns the last error
// observed, with each attempt's failure logged to stderr so the
// operator can see what went wrong.
func startCloudflaredWithRetry(localURL string, debug bool) (*Tunnel, string, context.CancelFunc, error) {
	var lastErr error
	for attempt := 1; attempt <= cloudflaredMaxAttempts; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		t, publicURL, err := startCloudflaredFn(ctx, localURL)
		if err == nil {
			if attempt > 1 || debug {
				fmt.Fprintf(errLog(), "ccm share: cloudflared tunnel ready (attempt %d/%d)\n",
					attempt, cloudflaredMaxAttempts)
			}
			return t, publicURL, cancel, nil
		}
		cancel()
		lastErr = err
		fmt.Fprintf(errLog(), "ccm share: cloudflared attempt %d/%d failed: %v\n",
			attempt, cloudflaredMaxAttempts, err)
		if attempt < cloudflaredMaxAttempts {
			time.Sleep(cloudflaredRetryBackoff(attempt))
		}
	}
	return nil, "", nil, fmt.Errorf("cloudflared failed after %d attempts: %w",
		cloudflaredMaxAttempts, lastErr)
}

// SetUpstreamBaseForTest overrides the constant upstreamBase used
// by NewProxy so tests can point the reverse proxy at a local
// httptest server. Returns the previous value.
func SetUpstreamBaseForTest(url string) string {
	orig := upstreamBaseOverride
	upstreamBaseOverride = url
	return orig
}

func ResetUpstreamBaseForTest() {
	upstreamBaseOverride = ""
}

func SetCaptureFnForTest(fn func(*Proxy, string) error) {
	captureFn = fn
}

func ResetCaptureFnForTest() {
	captureFn = runCapture
}

// SetCaptureCredFnForTest overrides captureCredFn for the duration
// of a test. Returns a restorer the caller can defer.
func SetCaptureCredFnForTest(fn func(*store.Credential, string) (http.Header, error)) func() {
	orig := captureCredFn
	captureCredFn = fn
	return func() { captureCredFn = orig }
}

func SetCloudflaredFnForTest(fn func(context.Context, string) (*Tunnel, string, error)) {
	startCloudflaredFn = fn
}

func ResetCloudflaredFnForTest() {
	startCloudflaredFn = startCloudflared
}

func NewFakeClockForTest(now time.Time) *FakeClock {
	return &FakeClock{inner: newFakeClock(now)}
}

// FakeClock is the test seam for the share package's clock interface,
// exported so cross-package tests (cmd/share_loadbalance_acceptance_test)
// can pass a deterministic clock through Options.Clock.
type FakeClock struct{ inner *fakeClock }

func (f *FakeClock) Advance(d time.Duration)               { f.inner.Advance(d) }
func (f *FakeClock) Now() time.Time                        { return f.inner.Now() }
func (f *FakeClock) NewTimer(d time.Duration) clockTimer   { return f.inner.NewTimer(d) }
func (f *FakeClock) NewTicker(d time.Duration) clockTicker { return f.inner.NewTicker(d) }

// PoolBackgroundOptions carries the public knobs for
// StartPoolBackground. Capture/clock seams stay package-internal —
// callers select share-mode vs launch-mode via SkipCapture.
type PoolBackgroundOptions struct {
	RebalanceInterval time.Duration
	Debug             bool
	SkipCapture       bool   // true → scheduler skipCapture=true (launch)
	Prompt            string // unused when SkipCapture
}

// StartPoolBackground spawns per-cred refresh timers + the rotation
// scheduler goroutine. They exit when done is closed (typically the
// channel returned by LocalProxy.Done()). Pre-flight: if pool.Fresh()
// fails synchronously, the scheduler's runOnce is invoked once to
// attempt rotation; a second pool.Fresh() failure surfaces as a fatal
// error from this function.
//
// The pre-flight scheduler is the SAME scheduler instance used by the
// long-running goroutine. This matters: it inherits skipCapture,
// prompt, and debug from opts so a launch-mode pre-flight does not
// accidentally call captureCredFn (which would fail because there is
// no spawned claude to drive an ephemeral capture).
func StartPoolBackground(done <-chan struct{}, pool *credPool, opts PoolBackgroundOptions) error {
	c := clock(realClock{})

	// Build the scheduler ONCE, configured per opts. Used both for
	// pre-flight (synchronous runOnce) and the long-running goroutine.
	sch := newScheduler(pool, productionProbe, c, opts.RebalanceInterval)
	sch.skipCapture = opts.SkipCapture
	sch.prompt = opts.Prompt
	sch.SetDebug(opts.Debug)

	if _, err := pool.Fresh(); err != nil {
		sch.runOnce()
		if _, err := pool.Fresh(); err != nil {
			return fmt.Errorf("pool pre-flight: no usable credential: %w", err)
		}
	}

	for _, e := range pool.entries {
		state := e.state
		go runRefreshTimer(state, c, jitterFn, done)
	}

	go sch.Run(done)

	// Tests can grab the scheduler via lastSchedulerForTest below.
	lastSchedulerForTest.Store(sch)

	return nil
}

// lastSchedulerForTest records the most recently constructed
// scheduler from StartPoolBackground so tests can observe TickDone()
// without restructuring the public API. Production code never reads
// this value.
var lastSchedulerForTest atomic.Pointer[scheduler]

// LastSchedulerTickDoneForTest returns the TickDone channel of the
// most recently constructed scheduler from StartPoolBackground.
// Test-only. Returns nil if StartPoolBackground has never been
// called.
func LastSchedulerTickDoneForTest() <-chan struct{} {
	if s := lastSchedulerForTest.Load(); s != nil {
		return s.TickDone()
	}
	return nil
}

// ResetLastSchedulerForTest clears the test seam so a fresh test
// run does not observe a previous test's scheduler. Call from
// t.Cleanup or at test start.
func ResetLastSchedulerForTest() {
	lastSchedulerForTest.Store(nil)
}

// LaunchExec is the shape of the launch-time exec call. Production
// uses exec.Command(...).Run with stdin/stdout/stderr inherited;
// tests can swap via SetLaunchExecFnForTest to synthesize HTTP
// traffic through the proxy without spawning a real claude binary.
type LaunchExec func(name string, args []string, env []string) error

var launchExecFn LaunchExec = func(name string, args []string, env []string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// LaunchExecFn returns the current launch exec function. Used by
// cmd/launch.go to invoke claude.
func LaunchExecFn() LaunchExec { return launchExecFn }

// SetLaunchExecFnForTest overrides the launch exec for the duration
// of a test. Returns a restorer the caller can defer.
func SetLaunchExecFnForTest(fn LaunchExec) func() {
	orig := launchExecFn
	launchExecFn = fn
	return func() { launchExecFn = orig }
}
