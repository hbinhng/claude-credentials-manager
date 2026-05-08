package capture_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/capture"
	"github.com/hbinhng/claude-credentials-manager/internal/codex/transport"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// newTransport returns an insecure transport suitable for tests that hit
// httptest servers.
func newTransport(t *testing.T) *transport.Transport {
	t.Helper()
	tr, err := transport.New(transport.Options{
		ProfileName:        transport.Default,
		InsecureSkipVerify: true,
		Timeout:            10 * time.Second,
	})
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	return tr
}

// newCodexCred builds a minimal codex credential for testing.
func newCodexCred() *store.Credential {
	return &store.Credential{
		Provider: "codex",
		Tokens:   &store.CodexTokens{AccessToken: "test-token", AccountID: "acc-1"},
	}
}

// writeStub writes a codex stub shell script to a temp dir and returns its path.
// The stub POSTs a fixed codex-shaped body to the openai_base_url passed via --config.
// The test must prepend filepath.Dir(stub) to PATH.
//
// Note: curl must be available in the test environment. This is standard on
// Linux/macOS; minimal Docker containers (e.g. distroless) lack it. If curl
// is missing, skip tests that depend on this stub.
func writeStub(t *testing.T, scriptBody string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return script
}

// defaultStubScript is the standard codex stub that sends a well-formed
// codex-shaped request.
const defaultStubScript = `#!/usr/bin/env bash
URL=""
for arg in "$@"; do
  case "$arg" in
    openai_base_url=*) URL="${arg#openai_base_url=}" ;;
  esac
done
[[ -z "$URL" ]] && { echo "no base URL" >&2; exit 1; }
curl -s -X POST "$URL/responses" \
  -H 'originator: codex_cli_rs' \
  -H 'session_id: stub-sess-1' \
  -H 'User-Agent: codex-cli/0.129.0' \
  -d '{"model":"gpt-5","input":[],"service_tier":"priority","client_metadata":{"x-codex-installation-id":"inst-stub"},"session_id":"stub-sess-1"}' >/dev/null 2>&1
exit 0
`

// fakeUpstreamServer returns an httptest.Server that responds to POST /v1/responses
// with the provided handler, and closes it via t.Cleanup.
func fakeUpstreamServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/responses", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// pathWithStub returns a PATH value that prepends dir containing the stub
// in front of the existing PATH so bash, curl, etc. remain reachable.
func pathWithStub(stubPath string) string {
	return filepath.Dir(stubPath) + string(os.PathListSeparator) + os.Getenv("PATH")
}

// checkCurlAvailable skips the test if curl is not available on the host PATH.
// The stub scripts use curl; tests that exercise the stub need it.
// Note: we must look up curl on the host PATH *before* t.Setenv overrides it,
// so we use exec.LookPath directly here (which reads PATH at call time).
func checkCurlAvailable(t *testing.T) {
	t.Helper()
	// Use exec.LookPath with the current (pre-Setenv) PATH.
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available in test environment — skipping stub-based test")
	}
}

// TestRun_CodexNotOnPATH verifies that Run returns ErrCodexCLINotFound when
// codex is absent from PATH. We set PATH to an empty temp dir that contains
// no executables.
func TestRun_CodexNotOnPATH(t *testing.T) {
	emptyDir := t.TempDir()
	// Replace PATH entirely with an empty dir — LookPath("codex") will fail.
	// bash/curl are not needed in this path because we error out before spawn.
	t.Setenv("PATH", emptyDir)

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:      newCodexCred(),
		Transport: tr,
		Timeout:   5 * time.Second,
	})
	if !errors.Is(err, capture.ErrCodexCLINotFound) {
		t.Errorf("err = %v, want ErrCodexCLINotFound", err)
	}
}

// TestRun_CodexStubCaptures is the happy path: stub codex POSTs a well-formed
// body, fake upstream returns 200, capture succeeds.
func TestRun_CodexStubCaptures(t *testing.T) {
	checkCurlAvailable(t)

	stub := writeStub(t, defaultStubScript)
	t.Setenv("PATH", pathWithStub(stub))

	// Fake upstream: accept POST, return 200 with empty body so curl exits cleanly.
	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	res, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     15 * time.Second,
		UpstreamURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify captured header bundle.
	if got := res.HeaderBundle.Get("originator"); got != "codex_cli_rs" {
		t.Errorf("originator = %q, want codex_cli_rs", got)
	}
	if got := res.HeaderBundle.Get("user-agent"); got != "codex-cli/0.129.0" {
		t.Errorf("User-Agent = %q, want codex-cli/0.129.0", got)
	}

	// Verify extracted body fields.
	if res.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %q, want priority", res.ServiceTier)
	}
	if res.SessionID != "stub-sess-1" {
		t.Errorf("SessionID = %q, want stub-sess-1", res.SessionID)
	}
	if res.InstallationID != "inst-stub" {
		t.Errorf("InstallationID = %q, want inst-stub", res.InstallationID)
	}
	if len(res.RawBody) == 0 {
		t.Error("RawBody should not be empty")
	}
}

// TestRun_UpstreamForwardFails checks that when the fake upstream returns a
// non-2xx status, Run returns ErrUpstreamForward.
func TestRun_UpstreamForwardFails(t *testing.T) {
	checkCurlAvailable(t)

	stub := writeStub(t, defaultStubScript)
	t.Setenv("PATH", pathWithStub(stub))

	// Fake upstream returns 502.
	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     15 * time.Second,
		UpstreamURL: upstream.URL,
	})
	if !errors.Is(err, capture.ErrUpstreamForward) {
		t.Errorf("err = %v, want ErrUpstreamForward", err)
	}
}

// TestRun_CodexSpawnTimeout verifies that a stub that never makes a request
// (sleeps forever) triggers ErrCodexSpawnTimeout.
func TestRun_CodexSpawnTimeout(t *testing.T) {
	const sleepScript = `#!/usr/bin/env bash
sleep 9999
`
	stub := writeStub(t, sleepScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	// Very short timeout so the test doesn't block long.
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     500 * time.Millisecond,
		UpstreamURL: upstream.URL,
	})
	if !errors.Is(err, capture.ErrCodexSpawnTimeout) {
		t.Errorf("err = %v, want ErrCodexSpawnTimeout", err)
	}
}

// TestRun_CodexExitNonZero tests a stub that exits non-zero without making
// any request to the capture server.
func TestRun_CodexExitNonZero(t *testing.T) {
	const failScript = `#!/usr/bin/env bash
exit 1
`
	stub := writeStub(t, failScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     10 * time.Second,
		UpstreamURL: upstream.URL,
	})
	// A stub that exits non-zero without making a request is treated as
	// ErrCodexExitNonZero. A clean exit (exit 0) without a request hits the
	// ErrCodexSpawnTimeout path, since no capture event fires.
	if !errors.Is(err, capture.ErrCodexExitNonZero) {
		t.Errorf("err = %v, want ErrCodexExitNonZero", err)
	}
}

// TestRun_BodyParseFailure tests a stub that POSTs malformed JSON body, which
// should trigger ErrCaptureBodyParse.
func TestRun_BodyParseFailure(t *testing.T) {
	checkCurlAvailable(t)

	const badJSONScript = `#!/usr/bin/env bash
URL=""
for arg in "$@"; do
  case "$arg" in
    openai_base_url=*) URL="${arg#openai_base_url=}" ;;
  esac
done
[[ -z "$URL" ]] && { echo "no base URL" >&2; exit 1; }
curl -s -X POST "$URL/responses" \
  -H 'Content-Type: application/json' \
  -d 'not-valid-json{{{{' >/dev/null 2>&1
exit 0
`
	stub := writeStub(t, badJSONScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     15 * time.Second,
		UpstreamURL: upstream.URL,
	})
	if !errors.Is(err, capture.ErrCaptureBodyParse) {
		t.Errorf("err = %v, want ErrCaptureBodyParse", err)
	}
}

// TestRun_CodexExitCleanWithoutRequest verifies that a stub that exits 0
// without making any request is treated as ErrCodexSpawnTimeout (no capture
// fired).
func TestRun_CodexExitCleanWithoutRequest(t *testing.T) {
	const cleanExitScript = `#!/usr/bin/env bash
exit 0
`
	stub := writeStub(t, cleanExitScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     10 * time.Second,
		UpstreamURL: upstream.URL,
	})
	// Clean exit without capture is treated as spawn timeout (no request fired).
	if !errors.Is(err, capture.ErrCodexSpawnTimeout) {
		t.Errorf("err = %v, want ErrCodexSpawnTimeout", err)
	}
}

// TestRun_DefaultTimeout verifies that a zero Timeout defaults to 30s (doesn't
// panic or error due to a missing Timeout). We use a stub that succeeds
// immediately so the test doesn't actually wait 30s.
func TestRun_DefaultTimeout(t *testing.T) {
	checkCurlAvailable(t)

	stub := writeStub(t, defaultStubScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	// Timeout: 0 — should default to 30s internally; stub exits quickly.
	res, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		UpstreamURL: upstream.URL,
		// Timeout left as zero — exercises the default-30s branch.
	})
	if err != nil {
		t.Fatalf("Run with default timeout: %v", err)
	}
	if res.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %q, want priority", res.ServiceTier)
	}
}

// TestRun_NilTransportUsesDefaultClient verifies that passing Transport=nil
// causes Run to use http.DefaultClient for the upstream forward. We point
// UpstreamURL at a local httptest server so DefaultClient can reach it.
func TestRun_NilTransportUsesDefaultClient(t *testing.T) {
	checkCurlAvailable(t)

	stub := writeStub(t, defaultStubScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Transport is nil — exercises the http.DefaultClient.Do branch.
	res, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   nil, // explicit nil
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     15 * time.Second,
		UpstreamURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("Run with nil transport: %v", err)
	}
	if res.SessionID != "stub-sess-1" {
		t.Errorf("SessionID = %q, want stub-sess-1", res.SessionID)
	}
}

// TestRun_UpstreamNetworkError verifies that a connection-refused upstream
// (network error, not a status error) returns ErrUpstreamForward.
func TestRun_UpstreamNetworkError(t *testing.T) {
	checkCurlAvailable(t)

	stub := writeStub(t, defaultStubScript)
	t.Setenv("PATH", pathWithStub(stub))

	tr := newTransport(t)
	// Point to a port that is guaranteed to refuse connections.
	// We start a server and immediately close it to get a valid-but-dead address.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close() // now connection-refused

	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     15 * time.Second,
		UpstreamURL: deadURL,
	})
	if !errors.Is(err, capture.ErrUpstreamForward) {
		t.Errorf("err = %v, want ErrUpstreamForward (network error)", err)
	}
}

// TestRun_CodexExitNonZeroAfterCapture tests the path where the capture fires
// successfully but codex exits non-zero afterward.
func TestRun_CodexExitNonZeroAfterCapture(t *testing.T) {
	checkCurlAvailable(t)

	// Stub makes the request (capture fires) but exits 42 afterward.
	const stubScript = `#!/usr/bin/env bash
URL=""
for arg in "$@"; do
  case "$arg" in
    openai_base_url=*) URL="${arg#openai_base_url=}" ;;
  esac
done
[[ -z "$URL" ]] && { echo "no base URL" >&2; exit 1; }
curl -s -X POST "$URL/responses" \
  -H 'originator: codex_cli_rs' \
  -H 'session_id: stub-sess-1' \
  -H 'User-Agent: codex-cli/0.129.0' \
  -d '{"model":"gpt-5","input":[],"service_tier":"priority","client_metadata":{"x-codex-installation-id":"inst-stub"},"session_id":"stub-sess-1"}' >/dev/null 2>&1
exit 42
`
	stub := writeStub(t, stubScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     15 * time.Second,
		UpstreamURL: upstream.URL,
	})
	// Capture fired, but codex exited non-zero → ErrCodexExitNonZero.
	if !errors.Is(err, capture.ErrCodexExitNonZero) {
		t.Errorf("err = %v, want ErrCodexExitNonZero", err)
	}
}

// TestRun_StartFails verifies that when exec.Start fails (binary exists on PATH
// but is not a valid executable), Run returns ErrCodexSpawnTimeout.
func TestRun_StartFails(t *testing.T) {
	dir := t.TempDir()
	// Write an invalid binary — has execute bit but no valid ELF/shebang.
	// exec.LookPath will find it; exec.Command.Start will fail with
	// "exec format error".
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("INVALID\x00"), 0o755); err != nil {
		t.Fatalf("write invalid binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Timeout:     5 * time.Second,
		UpstreamURL: upstream.URL,
	})
	if !errors.Is(err, capture.ErrCodexSpawnTimeout) {
		t.Errorf("err = %v, want ErrCodexSpawnTimeout", err)
	}
}

// TestRun_CaptureSuccessThenTimeout tests the path where capture fires
// successfully but the process then hangs, causing the timeout to kill it
// and return ErrCodexSpawnTimeout.
func TestRun_CaptureSuccessThenTimeout(t *testing.T) {
	checkCurlAvailable(t)

	// Stub makes the request (capture fires) then sleeps forever.
	const stubScript = `#!/usr/bin/env bash
URL=""
for arg in "$@"; do
  case "$arg" in
    openai_base_url=*) URL="${arg#openai_base_url=}" ;;
  esac
done
[[ -z "$URL" ]] && { echo "no base URL" >&2; exit 1; }
curl -s -X POST "$URL/responses" \
  -H 'originator: codex_cli_rs' \
  -H 'session_id: stub-sess-1' \
  -H 'User-Agent: codex-cli/0.129.0' \
  -d '{"model":"gpt-5","input":[],"service_tier":"priority","client_metadata":{"x-codex-installation-id":"inst-stub"},"session_id":"stub-sess-1"}' >/dev/null 2>&1
sleep 9999
`
	stub := writeStub(t, stubScript)
	t.Setenv("PATH", pathWithStub(stub))

	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     800 * time.Millisecond,
		UpstreamURL: upstream.URL,
	})
	// Capture fired but process timed out → ErrCodexSpawnTimeout.
	if !errors.Is(err, capture.ErrCodexSpawnTimeout) {
		t.Errorf("err = %v, want ErrCodexSpawnTimeout", err)
	}
}

// TestRun_UpstreamErrorAndTimeout verifies that when the upstream returns an
// error (capture fires with err) AND the process doesn't exit before timeout,
// Run kills the process and returns the capture error (not a timeout).
func TestRun_UpstreamErrorAndTimeout(t *testing.T) {
	checkCurlAvailable(t)

	// Stub that makes a request and then sleeps (simulates codex hanging
	// after a failed request).
	const hangAfterPostScript = `#!/usr/bin/env bash
URL=""
for arg in "$@"; do
  case "$arg" in
    openai_base_url=*) URL="${arg#openai_base_url=}" ;;
  esac
done
[[ -z "$URL" ]] && { echo "no base URL" >&2; exit 1; }
curl -s -X POST "$URL/responses" \
  -H 'originator: codex_cli_rs' \
  -d '{"model":"gpt-5","input":[],"service_tier":"priority","client_metadata":{"x-codex-installation-id":"inst-stub"},"session_id":"stub-sess-1"}' >/dev/null 2>&1
sleep 9999
`
	stub := writeStub(t, hangAfterPostScript)
	t.Setenv("PATH", pathWithStub(stub))

	// Upstream returns error to trigger ErrUpstreamForward.
	upstream := fakeUpstreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusBadGateway)
	})

	tr := newTransport(t)
	_, err := capture.Run(context.Background(), capture.Options{
		Cred:        newCodexCred(),
		Transport:   tr,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Timeout:     800 * time.Millisecond,
		UpstreamURL: upstream.URL,
	})
	// The capture error (ErrUpstreamForward) takes priority over timeout.
	if !errors.Is(err, capture.ErrUpstreamForward) {
		t.Errorf("err = %v, want ErrUpstreamForward", err)
	}
}
