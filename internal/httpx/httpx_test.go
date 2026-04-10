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

// TestTransportValidSchemes exercises each accepted proxy scheme and
// asserts that Transport().Proxy resolves to the parsed URL for a
// representative request. Userinfo (user:pass@host) is covered as its
// own case because it's parsed into the URL struct specially and
// surfaces via the transport's Proxy-Authorization header.
func TestTransportValidSchemes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"http", "http://proxy.example:8080", "http://proxy.example:8080"},
		{"https", "https://proxy.example:8443", "https://proxy.example:8443"},
		{"socks5", "socks5://proxy.example:1080", "socks5://proxy.example:1080"},
		{"socks5h", "socks5h://proxy.example:1080", "socks5h://proxy.example:1080"},
		{"userinfo", "http://user:pass@proxy.example:8080", "http://user:pass@proxy.example:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CCM_PROXY", tc.raw)
			if err := httpx.Configure(); err != nil {
				t.Fatalf("Configure: %v", err)
			}

			tr, ok := httpx.Transport().(*http.Transport)
			if !ok {
				t.Fatalf("Transport() returned %T, want *http.Transport", httpx.Transport())
			}
			if tr.Proxy == nil {
				t.Fatalf("expected Proxy to be set for %s", tc.raw)
			}
			req := httptest.NewRequest("GET", "https://api.anthropic.com/v1/messages", nil)
			proxyURL, err := tr.Proxy(req)
			if err != nil {
				t.Fatalf("tr.Proxy: %v", err)
			}
			if proxyURL == nil {
				t.Fatalf("expected non-nil proxy URL for %s", tc.raw)
			}
			if proxyURL.String() != tc.want {
				t.Errorf("got %q, want %q", proxyURL.String(), tc.want)
			}
		})
	}
}
