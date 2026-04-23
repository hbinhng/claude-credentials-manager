package share

import (
	"context"
	"errors"
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
	captureFn = func(p *Proxy) error {
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

// avoid unused-import errors for errors package until tasks 4-5 add failure tests
var _ = errors.New
