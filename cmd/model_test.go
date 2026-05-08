package cmd

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestModelDiscovery_HappyPath uses a stub `claude` shell script that
// mirrors what the real claude binary does at the wire level: it reads
// $ANTHROPIC_BASE_URL, POSTs a minimal /v1/messages body containing the
// model field, and exits when it sees a complete SSE stream back.
//
// The stub is deliberately simple — curl + a here-doc body — because we
// only need to exercise the proxy's capture path, not real Anthropic
// model resolution. Skipped on Windows since the stub is bash-based.
func TestModelDiscovery_HappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash stub is unix-only; Windows users get manual verification per the release checklist")
	}
	stubDir := writeClaudeStubModelDiscovery(t)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	defer SetModelDiscoveryTimeoutForTest(5 * time.Second)()

	// runDiscovery invokes the cobra command with a captured stdout.
	out, err := runDiscovery(t, "claude-opus-4-5-PROBE")
	if err != nil {
		t.Fatalf("runDiscovery: %v", err)
	}
	if !strings.Contains(out, "claude-opus-4-5-PROBE") {
		t.Errorf("output missing model from stub: %q", out)
	}
	if !strings.Contains(out, "claude --model") {
		t.Errorf("output missing prefix line: %q", out)
	}
}

// TestModelDiscovery_NoClaudeBinary verifies the hard-fail with install
// hint when claude isn't on PATH.
func TestModelDiscovery_NoClaudeBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir; LookPath fails
	defer SetModelDiscoveryTimeoutForTest(5 * time.Second)()

	_, err := runDiscovery(t, "opus")
	if err == nil {
		t.Fatal("want error when claude is not on PATH")
	}
	if !strings.Contains(err.Error(), "claude CLI not found") {
		t.Errorf("error should mention 'claude CLI not found': %v", err)
	}
}

// TestModelDiscovery_ClaudeExitsNonZeroWithoutRequest covers the
// exitCh-with-non-nil-error branch in runModelDiscovery.
func TestModelDiscovery_ClaudeExitsNonZeroWithoutRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash stub is unix-only")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte("#!/usr/bin/env bash\nexit 7\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer SetModelDiscoveryTimeoutForTest(5 * time.Second)()

	_, err := runDiscovery(t, "opus")
	if err == nil {
		t.Fatal("want error when claude exits non-zero without posting")
	}
	if !strings.Contains(err.Error(), "without sending a request") {
		t.Errorf("error should mention missing request: %v", err)
	}
}

// TestModelDiscovery_ClaudeExitsCleanWithoutRequest covers the
// exitCh-with-nil-error branch in runModelDiscovery: a claude that
// exits 0 immediately without ever POSTing.
func TestModelDiscovery_ClaudeExitsCleanWithoutRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash stub is unix-only")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer SetModelDiscoveryTimeoutForTest(5 * time.Second)()

	_, err := runDiscovery(t, "opus")
	if err == nil {
		t.Fatal("want error when claude exits without posting")
	}
	if !strings.Contains(err.Error(), "without sending a request") {
		t.Errorf("error should mention missing request: %v", err)
	}
}

// TestModelDiscovery_Timeout verifies the timeout path when the stub
// claude doesn't make a request within the configured budget.
func TestModelDiscovery_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash stub is unix-only")
	}
	// Stub that sleeps without ever POSTing — exercises the ctx.Done branch.
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	body := "#!/usr/bin/env bash\nsleep 5\nexit 0\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer SetModelDiscoveryTimeoutForTest(500 * time.Millisecond)()

	_, err := runDiscovery(t, "opus")
	if err == nil {
		t.Fatal("want timeout error when claude never posts")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention 'timeout': %v", err)
	}
}

// runDiscovery invokes the cobra command with --from set to model and
// returns the captured stdout plus the run error.
func runDiscovery(t *testing.T, model string) (string, error) {
	t.Helper()
	// Reset flag state across tests since cobra's flag values are global.
	prev := modelDiscoveryFrom
	modelDiscoveryFrom = model
	t.Cleanup(func() { modelDiscoveryFrom = prev })

	var out bytes.Buffer
	modelDiscoveryCmd.SetOut(&out)
	modelDiscoveryCmd.SetErr(&out)
	t.Cleanup(func() {
		modelDiscoveryCmd.SetOut(nil)
		modelDiscoveryCmd.SetErr(nil)
	})

	err := runModelDiscovery(modelDiscoveryCmd, nil)
	return out.String(), err
}

// TestModelDiscovery_SyntheticSSE_EmptyModel covers the empty-model
// fallback in writeSyntheticAnthropicSSE.
func TestModelDiscovery_SyntheticSSE_EmptyModel(t *testing.T) {
	w := &recordingResponseWriter{header: http.Header{}}
	writeSyntheticAnthropicSSE(w, "")
	body := w.body.String()
	if !strings.Contains(body, `"model":"unknown"`) {
		t.Errorf("empty model should emit unknown fallback; body = %q", body)
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Errorf("body should end with message_stop event: %q", body)
	}
}

// TestModelDiscovery_JSONEscape covers the escape branches in jsonEscape.
func TestModelDiscovery_JSONEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{`with"quote`, `with\"quote`},
		{`with\backslash`, `with\\backslash`},
		{`both"\done`, `both\"\\done`},
	}
	for _, c := range cases {
		got := jsonEscape(c.in)
		if got != c.want {
			t.Errorf("jsonEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// recordingResponseWriter is a tiny stub for unit-testing the SSE writer
// without httptest's network plumbing.
type recordingResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
}

func (r *recordingResponseWriter) Header() http.Header        { return r.header }
func (r *recordingResponseWriter) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recordingResponseWriter) WriteHeader(s int)            { r.statusCode = s }

// writeClaudeStubModelDiscovery creates a minimal `claude` shell script
// that:
//  1. Reads $ANTHROPIC_BASE_URL.
//  2. POSTs a /v1/messages body whose model field equals the value
//     passed to `--model`.
//  3. Drains the SSE response from the proxy (so the proxy's flush
//     completes) and exits 0.
//
// The model value passed via --model is echoed verbatim into the body's
// "model" field so the test can assert on it. The stub deliberately does
// NOT do any real model-resolution; that's the discovery feature's job
// in production via the real claude binary.
func writeClaudeStubModelDiscovery(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	body := `#!/usr/bin/env bash
set -e

MODEL=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) MODEL="$2"; shift 2 ;;
    -p)      shift 2 ;;
    *)       shift ;;
  esac
done

[[ -z "$MODEL" ]] && { echo "stub: --model required" >&2; exit 1; }
[[ -z "$ANTHROPIC_BASE_URL" ]] && { echo "stub: ANTHROPIC_BASE_URL required" >&2; exit 1; }

# Drain SSE response to /dev/null so curl exits when the proxy closes the stream.
curl -s -N -X POST "$ANTHROPIC_BASE_URL/v1/messages" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}" \
  >/dev/null
exit 0
`
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return stub
}
