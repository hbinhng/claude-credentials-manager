package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestConfigureErrors asserts that malformed CCM_PROXY values produce
// a descriptive, actionable error from Configure. Each case checks
// that the error message contains a distinctive substring so a user
// reading the error can tell what was wrong.
func TestConfigureErrors(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		substr string // expected substring in err.Error()
	}{
		{"missing-scheme", "not_a_url", "missing scheme"},
		{"missing-host-http", "http://", "missing host"},
		{"missing-host-socks5", "socks5://", "missing host"},
		{"missing-host-port-only", "http://:8080", "missing host"},
		{"unsupported-scheme", "ftp://proxy.example:21", `unsupported scheme "ftp"`},
		{"unparseable", "http://[::1", "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CCM_PROXY", tc.raw)
			err := httpx.Configure()
			if err == nil {
				t.Fatalf("expected error for CCM_PROXY=%q, got nil", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.substr)
			}
		})
	}
}

// TestConfigureEmptyIsNotError confirms that CCM_PROXY set to the
// empty string is treated as "unset" — no error, falls back to the
// proxy-less transport.
func TestConfigureEmptyIsNotError(t *testing.T) {
	t.Setenv("CCM_PROXY", "")
	if err := httpx.Configure(); err != nil {
		t.Fatalf("Configure with empty CCM_PROXY: %v", err)
	}
	tr, ok := httpx.Transport().(*http.Transport)
	if !ok {
		t.Fatalf("Transport() returned %T", httpx.Transport())
	}
	if tr.Proxy != nil {
		req := httptest.NewRequest("GET", "https://api.anthropic.com/", nil)
		p, _ := tr.Proxy(req)
		if p != nil {
			t.Errorf("expected nil proxy, got %v", p)
		}
	}
}
