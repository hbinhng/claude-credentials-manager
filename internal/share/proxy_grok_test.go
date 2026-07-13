package share

import (
	"testing"

	grokmw "github.com/hbinhng/claude-credentials-manager/internal/grok/middleware"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// TestSetGrokHandlersWiresProvider verifies SetGrokHandlers marks the
// proxy as the grok provider and that cred() returns the grok
// credential (not codexCred) once wired.
func TestSetGrokHandlersWiresProvider(t *testing.T) {
	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer p.Close()

	grokCred := &store.Credential{
		ID:       "gid",
		Name:     "me@example.com",
		Provider: "grok",
		GrokTokens: &store.GrokTokens{
			AccessToken:  "acc",
			RefreshToken: "ref",
		},
	}

	p.SetGrokHandlers(GrokHandlers{
		Cred:        grokCred,
		Transport:   nil,
		UpstreamURL: "http://x",
	})

	if p.provider != "grok" {
		t.Errorf("provider = %q, want grok", p.provider)
	}
	if p.cred() != grokCred {
		t.Errorf("cred() = %v, want %v", p.cred(), grokCred)
	}
}

// TestTerminalForProviderGrok verifies terminalForProvider returns a
// *grokmw.Terminal when the provider is "grok", wired with the
// grokTransport/grokUpstreamURL/bearerSrc/handleSessionDie set on the
// proxy.
func TestTerminalForProviderGrok(t *testing.T) {
	p, err := NewProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer p.Close()

	grokCred := &store.Credential{
		ID:       "gid",
		Provider: "grok",
		GrokTokens: &store.GrokTokens{
			AccessToken:  "acc",
			RefreshToken: "ref",
		},
	}
	p.SetGrokHandlers(GrokHandlers{
		Cred:        grokCred,
		Transport:   nil,
		UpstreamURL: "http://x",
	})

	h := p.terminalForProvider()
	if _, ok := h.(*grokmw.Terminal); !ok {
		t.Fatalf("terminalForProvider() = %T, want *grokmw.Terminal", h)
	}
}
