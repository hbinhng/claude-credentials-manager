package share

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
		gotAgent string
		gotPath  string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotModel = string(b)
		gotAgent = r.Header.Get("x-grok-agent-id")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		q := `"hi"`
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"+
				"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n"+
				"data: {\"type\":\"response.output_text.delta\",\"delta\":"+string(q)+"}\n\n"+
				"data: {\"type\":\"response.output_text.done\"}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
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
	if gotPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", gotPath)
	}
	if gotAgent == "" {
		t.Error("x-grok-agent-id missing — CredentialID not wired")
	}
	if !strings.Contains(gotModel, "grok-4.7") {
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

// startGrokSessionAgainst starts a LAN-bound grok share session whose
// terminal talks to the given fake upstream, and returns a poster that
// sends an Anthropic body through the session's proxy under one Claude
// Code session id.
func startGrokSessionAgainst(t *testing.T, upstreamURL, credID string) func(body string) *http.Response {
	t.Helper()
	stubGrokSessionDeps(t)
	restore := SetGrokHandlersFnForTest(func(cred *store.Credential) (GrokHandlers, error) {
		return GrokHandlers{Cred: cred, Transport: http.DefaultClient, UpstreamURL: upstreamURL}, nil
	})
	t.Cleanup(restore)
	sess, err := StartSession(grokCred(t, credID), Options{BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Stop() })
	tk, err := DecodeTicket(sess.Ticket())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	return func(body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", sess.Reach()+"/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tk.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Claude-Code-Session-Id", "cc-sess-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/messages: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp
	}
}

// grokSSE is a minimal successful /v1/responses stream reporting
// inputTokens of usage.
func grokSSE(inputTokens int) string {
	return "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"m1\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"data: {\"type\":\"response.output_text.done\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":" + strconv.Itoa(inputTokens) + ",\"output_tokens\":1}}}\n\n"
}

const (
	grokTurn1 = `{"model":"claude-opus-4.7","messages":[{"role":"user","content":"run ls"}]}`
	grokTool  = `{"model":"claude-opus-4.7","messages":[{"role":"user","content":"run ls"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"ls"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"a.txt"}]}]}`
	grokTurn2 = `{"model":"claude-opus-4.7","messages":[{"role":"user","content":"run ls"},` +
		`{"role":"assistant","content":[{"type":"text","text":"a.txt"}]},` +
		`{"role":"user","content":"thanks, now pwd"}]}`
)

// TestStartSession_GrokTerminalStatePersistsAcrossRequests: the share
// proxy must reuse one grok Terminal for the session, so per-session
// state (user-turn counter) survives between requests. A user turn, its
// tool_result follow-up, then a new user turn → x-grok-turn-idx 1,1,2.
func TestStartSession_GrokTerminalStatePersistsAcrossRequests(t *testing.T) {
	var (
		mu   sync.Mutex
		idxs []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		idxs = append(idxs, r.Header.Get("x-grok-turn-idx"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, grokSSE(5))
	}))
	defer upstream.Close()

	post := startGrokSessionAgainst(t, upstream.URL, "grok-state-0000-0000-0000-000000000005")
	for _, b := range []string{grokTurn1, grokTool, grokTurn2} {
		if resp := post(b); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"1", "1", "2"}; !reflect.DeepEqual(idxs, want) {
		t.Errorf("x-grok-turn-idx sequence = %v, want %v", idxs, want)
	}
}

// TestStartSession_GrokProactiveCompactGateSurvivesRequests: usage at or
// above 92% of x-grok-context-window on one request must make the next
// request on the session return the "prompt is too long" 400 without
// reaching upstream — which only works if the terminal outlives a request.
func TestStartSession_GrokProactiveCompactGateSurvivesRequests(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("x-grok-context-window", "1000")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, grokSSE(950))
	}))
	defer upstream.Close()

	post := startGrokSessionAgainst(t, upstream.URL, "grok-state-0000-0000-0000-000000000006")
	if resp := post(grokTurn1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", resp.StatusCode)
	}
	if resp := post(grokTurn2); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("second status = %d, want 400 prompt is too long", resp.StatusCode)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("upstream hits = %d, want 1 (gated request must not reach upstream)", n)
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
