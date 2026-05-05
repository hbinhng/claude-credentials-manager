package share

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestCaptureCredHeadersHappyPath(t *testing.T) {
	cred := &store.Credential{ID: "11111111-1111-1111-1111-111111111111", Name: "alice"}

	// Stub captureFn to mark capture done with a fixed header set.
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(p *Proxy, _ string) error {
		p.markCaptured(http.Header{
			"User-Agent":       []string{"test-ua"},
			"Anthropic-Beta":   []string{"oauth-2025-04-20"},
			"X-Stainless-Lang": []string{"node"},
		})
		return nil
	}

	headers, err := captureCredHeaders(cred, "test-prompt")
	if err != nil {
		t.Fatalf("captureCredHeaders: %v", err)
	}
	if got := headers.Get("User-Agent"); got != "test-ua" {
		t.Errorf("User-Agent = %q, want test-ua", got)
	}
	if got := headers.Get("Anthropic-Beta"); got != "oauth-2025-04-20" {
		t.Errorf("Anthropic-Beta = %q, want oauth-2025-04-20", got)
	}
}

func TestCaptureCredHeadersPropagatesError(t *testing.T) {
	cred := &store.Credential{ID: "11111111-1111-1111-1111-111111111111", Name: "alice"}

	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(_ *Proxy, _ string) error {
		return errors.New("claude exited early")
	}

	_, err := captureCredHeaders(cred, "prompt")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "alice") {
		t.Errorf("error %q does not include cred name", err)
	}
	if !strings.Contains(err.Error(), "claude exited early") {
		t.Errorf("error %q does not include underlying cause", err)
	}
}

func TestCaptureCredHeadersTimeout(t *testing.T) {
	cred := &store.Credential{ID: "11111111-1111-1111-1111-111111111111", Name: "alice"}

	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	// Block forever.
	blocked := make(chan struct{})
	defer close(blocked)
	captureFn = func(_ *Proxy, _ string) error {
		<-blocked
		return nil
	}

	// Inject a small timeout so the test doesn't wait 60s.
	origTimeout := captureCredTimeout
	defer func() { captureCredTimeout = origTimeout }()
	captureCredTimeout = 100 * time.Millisecond

	start := time.Now()
	_, err := captureCredHeaders(cred, "prompt")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q does not say 'timed out'", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed %v, want < 500ms (timeout was 100ms)", elapsed)
	}
}

func TestCaptureCredFnDefault(t *testing.T) {
	// Default seam should point at captureCredHeaders.
	if fmt.Sprintf("%p", captureCredFn) != fmt.Sprintf("%p", captureCredHeaders) {
		t.Errorf("captureCredFn does not default to captureCredHeaders")
	}
}

func TestCaptureCredHeadersReleasesPort(t *testing.T) {
	cred := &store.Credential{ID: "11111111-1111-1111-1111-111111111111", Name: "alice"}

	// Capture the port the ephemeral proxy bound to, then verify
	// the same port is rebindable after the function returns.
	var capturedAddr string
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(p *Proxy, _ string) error {
		capturedAddr = p.Addr() // remember the port pre-teardown
		p.markCaptured(http.Header{"User-Agent": []string{"x"}})
		return nil
	}

	if _, err := captureCredHeaders(cred, "prompt"); err != nil {
		t.Fatalf("captureCredHeaders: %v", err)
	}

	// Parse the host:port out of the addr URL.
	u, perr := url.Parse(capturedAddr)
	if perr != nil {
		t.Fatalf("parse addr %q: %v", capturedAddr, perr)
	}
	// Try to bind to the same host:port. If the ephemeral proxy
	// did not release the port, this Listen will fail.
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		t.Fatalf("rebind %s after teardown: %v (port not released)", u.Host, err)
	}
	_ = ln.Close()
}
