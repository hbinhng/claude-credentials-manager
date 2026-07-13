package share

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
	"github.com/hbinhng/claude-credentials-manager/internal/trace"
)

// grokCred builds a minimal grok credential with a live (non-expired)
// access token so credState.Fresh() takes the cheap in-memory path and
// never calls credflow.RefreshFn, and persists it to the store (fake
// HOME set up by setupFakeHome, from credstate_test.go) so credState's
// reloadIfPeerWrote stat succeeds instead of logging a swallowed error.
func grokCred(t *testing.T, id string) *store.Credential {
	t.Helper()
	setupFakeHome(t)
	c := &store.Credential{
		ID:       id,
		Name:     "grok-test",
		Provider: "grok",
	}
	c.SetTokens("grok-acc", "grok-ref", time.Now().Add(time.Hour).UnixMilli())
	if err := store.Save(c); err != nil {
		t.Fatalf("store.Save grok cred: %v", err)
	}
	return c
}

// stubGrokSessionDeps installs the captureFn / cloudflared seams needed
// to drive StartSession end-to-end without a real `claude` binary or
// cloudflared process. Mirrors the stubbing session_test.go already does
// for the claude path.
func stubGrokSessionDeps(t *testing.T) {
	t.Helper()
	origCapture := captureFn
	captureFn = func(p *Proxy, _ string) error {
		p.markCaptured(captureHeadersForTest())
		return nil
	}
	t.Cleanup(func() { captureFn = origCapture })

	origTunnel := startCloudflaredFn
	startCloudflaredFn = func(_ context.Context, _ string) (*Tunnel, string, error) {
		return fakeTunnel(nil), "https://grok.example.trycloudflare.com", nil
	}
	t.Cleanup(func() { startCloudflaredFn = origTunnel })
}

// TestStartSession_GrokBranchWiresProxyAndRoutesUpstream is the
// smallest real (non-vacuous) exercise of the grok branch added to
// StartSession: it installs SetGrokHandlersFnForTest with a doer backed
// by a fake upstream, starts a session with a grok credential, and
// asserts a request through the session's proxy is actually answered by
// the fake grok upstream — proving SetGrokHandlers + SetBearerSource
// wired the terminal correctly, not just that the branch compiled.
func TestStartSession_GrokBranchWiresProxyAndRoutesUpstream(t *testing.T) {
	stubGrokSessionDeps(t)

	var (
		gotAuth  string
		gotModel string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotModel = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer upstream.Close()

	restore := SetGrokHandlersFnForTest(func(cred *store.Credential) (GrokHandlers, error) {
		return GrokHandlers{
			Cred:        cred,
			Transport:   http.DefaultClient,
			UpstreamURL: upstream.URL,
		}, nil
	})
	defer restore()

	cred := grokCred(t, "grok-branch-0000-0000-0000-000000000001")

	// LAN-bind mode: the proxy listens on a real loopback port so the
	// test can dial it directly, instead of depending on the fake
	// (unreachable) tunnel hostname stubGrokSessionDeps installs.
	sess, err := StartSession(cred, Options{BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Stop()

	if sess.CredID() != cred.ID {
		t.Errorf("CredID() = %q, want %q", sess.CredID(), cred.ID)
	}

	tk, err := DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}

	req, err := http.NewRequest("POST", sess.Reach()+"/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4.7","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tk.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// The fake grok upstream — not the claude director — must have
	// received the request, proving terminalForProvider() dispatched to
	// the grok Terminal.
	if gotAuth != "Bearer grok-acc" {
		t.Errorf("upstream Authorization = %q, want %q (bearer source wired via SetBearerSource)", gotAuth, "Bearer grok-acc")
	}
	if !strings.Contains(gotModel, "grok-composer-2.5-fast") {
		t.Errorf("upstream body = %q, want it to contain the grok default model (no alias rules configured)", gotModel)
	}

	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), `"role":"assistant"`) {
		t.Errorf("downstream body = %q, want the fake upstream's response relayed through", out)
	}
}

// TestStartSession_GrokHandlersError verifies that when grokHandlersFn
// fails, StartSession closes the proxy and returns a wrapped error
// instead of starting a broken session. Mirrors codex's equivalent
// error path via the same seam pattern.
func TestStartSession_GrokHandlersError(t *testing.T) {
	stubGrokSessionDeps(t)

	restore := SetGrokHandlersFnForTest(func(cred *store.Credential) (GrokHandlers, error) {
		return GrokHandlers{}, errors.New("grok handlers boom")
	})
	defer restore()

	cred := grokCred(t, "grok-branch-0000-0000-0000-000000000002")

	sess, err := StartSession(cred, Options{})
	if err == nil {
		if sess != nil {
			_ = sess.Stop()
		}
		t.Fatalf("StartSession succeeded; want grok handlers error")
	}
	if !strings.Contains(err.Error(), "grok handlers") {
		t.Errorf("err = %v, want to contain %q", err, "grok handlers")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want wrapped cause to survive", err)
	}
}

// TestGrokHandlersFn_TraceWrapsDoer verifies the production
// grokHandlersFn (not a test seam) routes its doer through
// trace.WrapDoer, per the requirement that CCM_TRACE wrapping for grok
// happens here (the proxy's terminalForProvider passes the transport
// through unwrapped for grok, unlike codex — see proxy.go
// terminalForProvider's "grok" case). With CCM_TRACE=1 set,
// trace.WrapDoer must return a distinct wrapping value rather than the
// bare *http.Client, proving the wrap call actually happened.
func TestGrokHandlersFn_TraceWrapsDoer(t *testing.T) {
	t.Setenv(trace.EnvVar, "1")

	cred := grokCred(t, "grok-factory-0000-0000-0000-000000000003")
	handlers, err := grokHandlersFn(cred)
	if err != nil {
		t.Fatalf("grokHandlersFn: %v", err)
	}
	if handlers.Cred != cred {
		t.Errorf("handlers.Cred = %v, want %v", handlers.Cred, cred)
	}
	if handlers.Transport == nil {
		t.Fatal("handlers.Transport is nil")
	}
	if _, ok := handlers.Transport.(*http.Client); ok {
		t.Error("handlers.Transport is a bare *http.Client with CCM_TRACE=1; want it wrapped by trace.WrapDoer")
	}
}

// TestGrokHandlersFn_NoTraceIsPlainClient verifies that with CCM_TRACE
// unset, grokHandlersFn's doer is the plain http.Client wrapping
// httpx.Transport() unchanged — trace.WrapDoer is a documented no-op
// passthrough in that case, matching the codex path's behavior.
func TestGrokHandlersFn_NoTraceIsPlainClient(t *testing.T) {
	t.Setenv(trace.EnvVar, "")

	cred := grokCred(t, "grok-factory-0000-0000-0000-000000000004")
	handlers, err := grokHandlersFn(cred)
	if err != nil {
		t.Fatalf("grokHandlersFn: %v", err)
	}
	if _, ok := handlers.Transport.(*http.Client); !ok {
		t.Errorf("handlers.Transport = %T, want *http.Client when CCM_TRACE is unset (WrapDoer no-op passthrough)", handlers.Transport)
	}
}
