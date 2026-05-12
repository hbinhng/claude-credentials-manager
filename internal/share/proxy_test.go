package share

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestListenerBindAddr encodes the --bind-host / --bind-port flag rules
// from `ccm share`: an empty bindHost keeps the listener on loopback
// only; any non-empty bindHost means "reachable from elsewhere" so the
// listener is bound to the wildcard address (0.0.0.0). bindPort == 0
// means "let the OS pick".
func TestListenerBindAddr(t *testing.T) {
	cases := []struct {
		name     string
		bindHost string
		bindPort int
		want     string
	}{
		{"default loopback random", "", 0, "127.0.0.1:0"},
		{"default loopback fixed", "", 8080, "127.0.0.1:8080"},
		{"wildcard random", "my-laptop.local", 0, "0.0.0.0:0"},
		{"wildcard fixed", "192.168.1.5", 8080, "0.0.0.0:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ListenerBindAddr(tc.bindHost, tc.bindPort)
			if got != tc.want {
				t.Errorf("ListenerBindAddr(%q, %d) = %q, want %q", tc.bindHost, tc.bindPort, got, tc.want)
			}
		})
	}
}

// TestNewProxyBindsToProvidedAddress verifies NewProxy honors the
// caller-supplied bind address. This exists because `ccm share
// --bind-host` needs to listen on 0.0.0.0 instead of the loopback-only
// default.
func TestNewProxyBindsToProvidedAddress(t *testing.T) {
	t.Run("loopback", func(t *testing.T) {
		p, err := NewProxy("127.0.0.1:0")
		if err != nil {
			t.Fatalf("NewProxy: %v", err)
		}
		defer p.Close()

		if !strings.HasPrefix(p.Addr(), "http://127.0.0.1:") {
			t.Errorf("Addr() = %q, want http://127.0.0.1:...", p.Addr())
		}
	})

	t.Run("wildcard", func(t *testing.T) {
		p, err := NewProxy("0.0.0.0:0")
		if err != nil {
			t.Fatalf("NewProxy: %v", err)
		}
		defer p.Close()

		// Addr() is the URL that LOCAL processes (the capture claude
		// child, and the cloudflared tunnel) use to reach the proxy,
		// so it must stay on loopback even when the listener is bound
		// to a wildcard address. Connecting to 0.0.0.0 is non-portable
		// (macOS refuses it outright), and a wildcard bind accepts
		// connections on 127.0.0.1 anyway.
		host, port, err := net.SplitHostPort(strings.TrimPrefix(p.Addr(), "http://"))
		if err != nil {
			t.Fatalf("SplitHostPort: %v", err)
		}
		if host != "127.0.0.1" {
			t.Errorf("Addr() host = %q, want 127.0.0.1 (listener on 0.0.0.0 must still expose a loopback URL)", host)
		}
		if port == "" || port == "0" {
			t.Errorf("Addr() has no port: %q", p.Addr())
		}
	})
}

func TestHandleServeRejectsLoop(t *testing.T) {
	p, _ := NewProxy("127.0.0.1:0")
	defer p.Close()
	p.accessToken = "secret"
	p.viaID = "loopABCD"
	p.captured = http.Header{}
	p.mode = modeServing

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Via", "1.1 ccm-share/loopABCD")
	rr := httptest.NewRecorder()
	p.handleServe(rr, req)

	if rr.Code != http.StatusLoopDetected {
		t.Errorf("code = %d, want 508 (LoopDetected)", rr.Code)
	}
}

func TestDirectorRoutesPassthroughToTicketHost(t *testing.T) {
	p, _ := NewProxy("127.0.0.1:0")
	defer p.Close()
	p.accessToken = "secret"
	p.viaID = "myProcess"
	p.captured = http.Header{}
	p.mode = modeServing

	pt := newPassthroughEntryState(Ticket{Scheme: "https", Host: "upstream.example", Token: "tk"})
	e := &poolEntry{state: pt, status: statusActivated}
	pool := makePool(pt.credID(), false, map[string]*poolEntry{pt.credID(): e})
	p.pool = pool

	req, _ := http.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(req.Context(), ctxKeyRealToken, "tk")
	req = req.WithContext(ctx)

	p.director(req)

	if req.URL.Host != "upstream.example" {
		t.Errorf("URL.Host = %q, want upstream.example", req.URL.Host)
	}
	if req.URL.Scheme != "https" {
		t.Errorf("URL.Scheme = %q, want https", req.URL.Scheme)
	}
	if !strings.Contains(req.Header.Get("Via"), "ccm-share/myProcess") {
		t.Errorf("Via header missing: %q", req.Header.Get("Via"))
	}
	if req.Header.Get("Authorization") != "Bearer tk" {
		t.Errorf("Authorization = %q", req.Header.Get("Authorization"))
	}
}

func TestDirectorRoutesLocalToAnthropic(t *testing.T) {
	p, _ := NewProxy("127.0.0.1:0")
	defer p.Close()
	p.accessToken = "secret"
	p.viaID = "myProcess"
	p.captured = http.Header{"X-Test-Captured": {"yes"}}
	p.mode = modeServing

	fst := &fakeTokenSource{token: "tk-local"}
	e := newEntry("a", "n", statusActivated, fst)
	e.captured = http.Header{"X-Test-Captured": {"yes"}}
	pool := makePool("a", false, map[string]*poolEntry{"a": e})
	p.pool = pool

	req, _ := http.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(req.Context(), ctxKeyRealToken, "tk-local")
	req = req.WithContext(ctx)

	p.director(req)

	if !strings.Contains(req.URL.Host, "anthropic.com") {
		t.Errorf("local cred should route to Anthropic; got %q", req.URL.Host)
	}
	if req.Header.Get("X-Test-Captured") != "yes" {
		t.Errorf("local cred should overlay captured headers")
	}
	if req.Header.Get("Via") != "" {
		t.Errorf("local cred should NOT add Via header; got %q", req.Header.Get("Via"))
	}
}
