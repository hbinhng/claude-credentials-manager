package share

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func fakeCred(id, token string) *store.Credential {
	return &store.Credential{
		ID: id,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  token,
			RefreshToken: "refresh-" + token,
			ExpiresAt:    time.Now().Add(1 * time.Hour).UnixMilli(),
		},
	}
}

func captureHeadersForTest() http.Header {
	return http.Header{
		"User-Agent":        []string{"test-agent"},
		"Anthropic-Version": []string{"2023-06-01"},
	}
}

func fakeTunnel(onStop func()) *Tunnel {
	return NewTunnelForTest(onStop)
}

func TestStartSession_TunnelHappyPath(t *testing.T) {
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(p *Proxy, _ string) error {
		p.markCaptured(captureHeadersForTest())
		return nil
	}

	origTunnel := startCloudflaredFn
	defer func() { startCloudflaredFn = origTunnel }()
	stopCalled := 0
	startCloudflaredFn = func(_ context.Context, localURL string) (*Tunnel, string, error) {
		if !strings.HasPrefix(localURL, "http://127.0.0.1:") {
			t.Errorf("cloudflared called with %q, want loopback URL", localURL)
		}
		return fakeTunnel(func() { stopCalled++ }), "https://example.trycloudflare.com", nil
	}

	cred := fakeCred("4300c4bc-c04d-4b1f-8609-6c7b518de3df", "tok-abc")

	sess, err := StartSession(cred, Options{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	if sess.CredID() != cred.ID {
		t.Errorf("CredID=%q, want %q", sess.CredID(), cred.ID)
	}
	if sess.Mode() != "tunnel" {
		t.Errorf("Mode=%q, want tunnel", sess.Mode())
	}
	if sess.Reach() != "https://example.trycloudflare.com" {
		t.Errorf("Reach=%q, want tunnel URL", sess.Reach())
	}
	if sess.Ticket() == "" {
		t.Errorf("Ticket is empty")
	}
	if sess.StartedAt().IsZero() {
		t.Errorf("StartedAt is zero")
	}
	if err := sess.Err(); err != nil {
		t.Errorf("Err()=%v, want nil", err)
	}

	if err := sess.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("Done did not close within 2s")
	}
	if stopCalled != 1 {
		t.Errorf("cloudflared stop called %d times, want 1", stopCalled)
	}

	// Stop() idempotency.
	if err := sess.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStartSession_LANBindMode(t *testing.T) {
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(p *Proxy, _ string) error {
		p.markCaptured(captureHeadersForTest())
		return nil
	}

	// LAN mode must not start cloudflared; fail the test if called.
	origTunnel := startCloudflaredFn
	defer func() { startCloudflaredFn = origTunnel }()
	startCloudflaredFn = func(_ context.Context, _ string) (*Tunnel, string, error) {
		t.Fatalf("cloudflared must not be started in LAN mode")
		return nil, "", nil
	}

	cred := fakeCred("lan-cred-0000-0000-0000-000000000001", "tok-lan")

	sess, err := StartSession(cred, Options{BindHost: "host.docker.internal"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	if sess.Mode() != "lan" {
		t.Errorf("Mode=%q, want lan", sess.Mode())
	}
	if !strings.HasPrefix(sess.Reach(), "http://host.docker.internal:") {
		t.Errorf("Reach=%q, want http://host.docker.internal:<port>", sess.Reach())
	}

	// Decode the ticket and verify the envelope carries the LAN dial target.
	tk, err := DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	if tk.Scheme != "http" {
		t.Errorf("ticket.Scheme=%q, want http", tk.Scheme)
	}
	if !strings.HasPrefix(tk.Host, "host.docker.internal:") {
		t.Errorf("ticket.Host=%q, want host.docker.internal:<port>", tk.Host)
	}
}

func TestStartSession_CaptureFailure(t *testing.T) {
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(_ *Proxy, _ string) error {
		return errors.New("capture boom")
	}

	origTunnel := startCloudflaredFn
	defer func() { startCloudflaredFn = origTunnel }()
	startCloudflaredFn = func(_ context.Context, _ string) (*Tunnel, string, error) {
		t.Fatalf("cloudflared must not run when capture fails")
		return nil, "", nil
	}

	cred := fakeCred("fail-0000-0000-0000-0000-000000000001", "tok")
	sess, err := StartSession(cred, Options{})
	if err == nil {
		if sess != nil {
			_ = sess.Stop()
		}
		t.Fatalf("StartSession succeeded; want capture error")
	}
	if !strings.Contains(err.Error(), "capture") {
		t.Errorf("err=%v, want to contain 'capture'", err)
	}
}

func TestStartSession_CloudflaredFailure(t *testing.T) {
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	captureFn = func(p *Proxy, _ string) error {
		p.markCaptured(captureHeadersForTest())
		return nil
	}

	origTunnel := startCloudflaredFn
	defer func() { startCloudflaredFn = origTunnel }()
	startCloudflaredFn = func(_ context.Context, _ string) (*Tunnel, string, error) {
		return nil, "", errors.New("cloudflared boom")
	}

	cred := fakeCred("fail-0000-0000-0000-0000-000000000002", "tok")
	sess, err := StartSession(cred, Options{})
	if err == nil {
		if sess != nil {
			_ = sess.Stop()
		}
		t.Fatalf("StartSession succeeded; want cloudflared error")
	}
	if !strings.Contains(err.Error(), "cloudflared") {
		t.Errorf("err=%v, want to contain 'cloudflared'", err)
	}
}

func TestStartSession_BindPortConflict(t *testing.T) {
	// Grab a port and hold it so the second NewProxy must fail.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("held listener: %v", err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	// No captureFn override needed: NewProxy fails before capture is reached.

	cred := fakeCred("bind-fail-0000-0000-0000-000000000003", "tok")
	sess, err := StartSession(cred, Options{BindHost: "127.0.0.1", BindPort: port})
	if err == nil {
		if sess != nil {
			_ = sess.Stop()
		}
		t.Fatalf("StartSession succeeded against a held port")
	}
}

func TestStartSession_ThreadsCapturePrompt(t *testing.T) {
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	got := ""
	captureFn = func(p *Proxy, prompt string) error {
		got = prompt
		p.markCaptured(captureHeadersForTest())
		return nil
	}

	origTunnel := startCloudflaredFn
	defer func() { startCloudflaredFn = origTunnel }()
	startCloudflaredFn = func(_ context.Context, _ string) (*Tunnel, string, error) {
		return fakeTunnel(nil), "https://example.trycloudflare.com", nil
	}

	cred := fakeCred("prompt-0000-0000-0000-0000-000000000001", "tok")
	sess, err := StartSession(cred, Options{CapturePrompt: "my custom prompt"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	if got != "my custom prompt" {
		t.Errorf("captureFn got prompt=%q, want %q", got, "my custom prompt")
	}
}

func TestStartSession_CapturePromptDefaults(t *testing.T) {
	origCapture := captureFn
	defer func() { captureFn = origCapture }()
	got := ""
	captureFn = func(p *Proxy, prompt string) error {
		got = prompt
		p.markCaptured(captureHeadersForTest())
		return nil
	}

	origTunnel := startCloudflaredFn
	defer func() { startCloudflaredFn = origTunnel }()
	startCloudflaredFn = func(_ context.Context, _ string) (*Tunnel, string, error) {
		return fakeTunnel(nil), "https://example.trycloudflare.com", nil
	}

	cred := fakeCred("prompt-default-0000-0000-0000-000000000002", "tok")
	sess, err := StartSession(cred, Options{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	if got != DefaultCapturePrompt {
		t.Errorf("captureFn got prompt=%q, want DefaultCapturePrompt %q", got, DefaultCapturePrompt)
	}
}
