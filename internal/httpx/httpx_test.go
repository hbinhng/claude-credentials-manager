package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/httpx"
)

// TestTransportUnsetIgnoresHttpsProxy verifies that when CCM_PROXY is
// not set, Transport() returns a transport whose Proxy field is nil —
// specifically, that HTTPS_PROXY set in the environment is NOT
// consulted (per the "CCM_PROXY is authoritative" rule).
func TestTransportUnsetIgnoresHttpsProxy(t *testing.T) {
	t.Setenv("CCM_PROXY", "")
	t.Setenv("HTTPS_PROXY", "http://should-be-ignored.example:9999")

	if err := httpx.Configure(); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	tr, ok := httpx.Transport().(*http.Transport)
	if !ok {
		t.Fatalf("Transport() returned %T, want *http.Transport", httpx.Transport())
	}
	if tr.Proxy != nil {
		req := httptest.NewRequest("GET", "https://api.anthropic.com/v1/messages", nil)
		proxyURL, err := tr.Proxy(req)
		if err != nil {
			t.Fatalf("tr.Proxy: %v", err)
		}
		if proxyURL != nil {
			t.Fatalf("expected no proxy when CCM_PROXY unset, got %v", proxyURL)
		}
	}
}
