package share

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// DefaultTunnelStartupTimeout caps how long we wait for cloudflared to
// print its Quick Tunnel URL. The process itself stays alive after that.
const DefaultTunnelStartupTimeout = 45 * time.Second

// trycloudflareRe matches the hostname emitted by `cloudflared tunnel
// --url`. Example: https://rubber-ide-preserve-samba.trycloudflare.com
var trycloudflareRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Tunnel is a running cloudflared Quick Tunnel.
type Tunnel struct {
	URL string // public https URL
	cmd *exec.Cmd

	waitOnce sync.Once
	waitErr  error
	done     chan struct{}

	// testCloseHook is called by Close() instead of the real subprocess
	// kill path when non-nil. Used only by NewTunnelForTest.
	testCloseHook func()

	// shutdownHook is called at the end of the real Close() path when
	// non-nil. Used by the session to attach a context-cancel func so
	// tunnel teardown propagates cancellation.
	shutdownHook func()
}

// StartTunnel launches `cloudflared tunnel --url http://127.0.0.1:<port>`,
// waits for the trycloudflare.com URL to appear in its logs, and returns
// a Tunnel handle.
//
// If cloudflared is not already installed, EnsureCloudflared downloads
// a pinned version to ~/.ccm/bin/.
//
// Caller must invoke Close() to stop the subprocess.
func StartTunnel(ctx context.Context, localURL string) (*Tunnel, error) {
	binary, err := EnsureCloudflared()
	if err != nil {
		return nil, err
	}

	// --protocol http2 avoids cloudflared's default QUIC path, which is
	// blocked (or severely degraded) in a lot of corporate / tailscale /
	// UDP-restricted networks. HTTP/2 is supported everywhere and comes
	// up in ~1 second vs. 60+ seconds of QUIC retries.
	cmd := exec.Command(binary, "tunnel", "--url", localURL, "--no-autoupdate", "--protocol", "http2")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe cloudflared stderr: %w", err)
	}
	// cloudflared also prints a few lines on stdout; capture and discard.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe cloudflared stdout: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cloudflared: %w", err)
	}

	t := &Tunnel{cmd: cmd, done: make(chan struct{})}

	// Reader goroutine: scan stderr for the URL, then drain the rest.
	urlC := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		// Large buffer — some cloudflared lines are wide.
		scanner.Buffer(make([]byte, 0, 16*1024), 1<<20)
		found := false
		for scanner.Scan() {
			line := scanner.Text()
			if !found {
				if m := trycloudflareRe.FindString(line); m != "" {
					found = true
					urlC <- m
				}
			}
			// Don't echo cloudflared chatter — it's very verbose. If the
			// user wants it they can run CLOUDFLARED_LOG=1 (future work).
		}
		if !found {
			urlC <- ""
		}
	}()

	// Wait for the URL to appear or the timeout to fire.
	startCtx, cancel := context.WithTimeout(ctx, DefaultTunnelStartupTimeout)
	defer cancel()

	select {
	case url := <-urlC:
		if url == "" {
			_ = cmd.Process.Kill()
			return nil, errors.New("cloudflared exited before emitting a tunnel URL")
		}
		t.URL = url
	case <-startCtx.Done():
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("timed out waiting for cloudflared tunnel URL after %s", DefaultTunnelStartupTimeout)
	}

	// Begin reaping the process in the background.
	go func() {
		t.waitErr = cmd.Wait()
		close(t.done)
	}()

	return t, nil
}

// WaitReady polls <tunnelURL>/ccm-share/healthz until it returns 200 or
// the timeout fires. Cloudflare Quick Tunnels print their public URL
// before the edge has propagated the route, which surfaces as error 1033
// on first hit; this sidesteps the race so callers can safely tell the
// user "live" only when the URL actually works.
func (t *Tunnel) WaitReady(ctx context.Context, timeout time.Duration) error {
	if t == nil || t.URL == "" {
		return errors.New("tunnel not started")
	}
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 10 * time.Second}
	url := t.URL + "/ccm-share/healthz"
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("tunnel never became ready: %w", err)
			}
			return errors.New("tunnel never became ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// Close sends SIGTERM to cloudflared and waits for it to exit.
func (t *Tunnel) Close() error {
	if t == nil {
		return nil
	}
	// Test-only shortcut: if a testCloseHook is set, use it instead of
	// the real subprocess kill. This lets tests verify Stop() behaviour
	// without spawning cloudflared.
	if t.testCloseHook != nil {
		t.testCloseHook()
		return nil
	}
	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	t.waitOnce.Do(func() {
		_ = t.cmd.Process.Signal(os.Interrupt)
		select {
		case <-t.done:
		case <-time.After(5 * time.Second):
			_ = t.cmd.Process.Kill()
			<-t.done
		}
		if t.shutdownHook != nil {
			t.shutdownHook()
		}
	})
	return t.waitErr
}

// PublicURL returns the https://… Cloudflare Quick Tunnel address.
// It is the same value as the URL field; the accessor exists so
// session.go can call it without referring to the struct field directly.
func (t *Tunnel) PublicURL() string {
	return t.URL
}

// NewTunnelForTest returns a Tunnel that does not spawn a cloudflared
// subprocess. Its Close() invokes onClose (if non-nil) and returns
// nil. Use only in tests.
//
// Close() is NOT idempotent at the Tunnel level — calling it twice
// runs onClose twice. Callers that need idempotency (e.g. sessionImpl)
// wrap Close in sync.Once themselves.
func NewTunnelForTest(onClose func()) *Tunnel {
	return &Tunnel{testCloseHook: onClose}
}

// setShutdownHook attaches fn to the tunnel so that it is called when
// the real cloudflared process exits (at the end of Close()). Used by
// the session to propagate cancellation when the tunnel dies.
func (t *Tunnel) setShutdownHook(fn func()) {
	t.shutdownHook = fn
}
