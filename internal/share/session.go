package share

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

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

	accessToken, err := newAccessToken()
	if err != nil {
		_ = proxy.Close()
		// coverage: unreachable — crypto/rand only errors on a kernel RNG
		// failure, which is not exercisable in tests.
		return nil, err
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
		ctx, cancel := context.WithCancel(context.Background())
		t, publicURL, err := startCloudflaredFn(ctx, proxy.Addr())
		if err != nil {
			cancel()
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
