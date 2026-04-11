package share

import (
	"net"
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
