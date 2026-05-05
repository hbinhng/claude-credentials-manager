package share

import (
	"fmt"
	"net/http"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// captureCredTimeout is the outer deadline for captureCredHeaders.
// Mirrors DefaultCaptureTimeout but is its own knob so tests can
// inject a smaller value without affecting the underlying RunCapture
// (which has its own subprocess context).
var captureCredTimeout = DefaultCaptureTimeout

// captureCredHeaders spawns an ephemeral loopback Proxy in CAPTURE
// mode, runs claude -p against it via the captureFn seam, and
// returns the recorded identity headers. The proxy is torn down on
// return.
//
// cred is metadata-only (used for log lines). The captured headers
// describe the local Claude Code install, not the upstream account
// — but capturing per-cred lets the rest of the pipeline treat
// each credential's headers as its own.
//
// The ephemeral proxy NEVER forwards to upstream: handleCapture
// returns synthetic 401 to claude. Capture consumes zero quota
// against the candidate's account.
func captureCredHeaders(cred *store.Credential, prompt string) (http.Header, error) {
	proxy, err := NewProxy("127.0.0.1:0")
	if err != nil {
		// coverage: unreachable — net.Listen on 127.0.0.1:0 only fails
		// in catastrophic OS conditions not exercisable in tests.
		return nil, fmt.Errorf("ephemeral capture proxy: %w", err)
	}

	go func() { _ = proxy.Start() }()

	// Snapshot the captureFn pointer under the same goroutine that
	// waits for completion, so the captureFn-goroutine's read is
	// happens-before-ordered with any test teardown that restores
	// the original captureFn.
	cfn := captureFn
	done := make(chan error, 1)
	go func() { done <- cfn(proxy, prompt) }()

	var capErr error
	timedOut := false
	select {
	case capErr = <-done:
	case <-time.After(captureCredTimeout):
		timedOut = true
	}

	// Close the proxy BEFORE returning so the listener port is
	// released and the captureFn goroutine (if still in-flight after
	// a timeout) sees a closed connection and exits. We do not block
	// on the goroutine here — RunCapture in production owns its own
	// subprocess context; in tests, a stub that blocks forever is
	// the test's responsibility to release before the next test
	// case runs.
	_ = proxy.Close()

	if timedOut {
		return nil, fmt.Errorf("capture %s: timed out after %s",
			credLogName(cred), captureCredTimeout)
	}
	if capErr != nil {
		return nil, fmt.Errorf("capture %s: %w", credLogName(cred), capErr)
	}
	return proxy.Captured(), nil
}

// captureCredFn is the test seam for the rest of the share package.
// Production points at captureCredHeaders; tests stub it to return
// canned headers (or an error) without spawning processes.
var captureCredFn = captureCredHeaders
