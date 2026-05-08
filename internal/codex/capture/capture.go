// Package capture spins up a local capture-mode HTTP listener, runs
// `codex exec "say hi" --config openai_base_url=<local>` once, and
// records the codex CLI's outbound headers + body fields. The recorded
// bundle is replayed verbatim across all subsequent translated requests
// in the same share/launch session.
//
// `codex exec` (rather than bare `codex "prompt"`) is required because
// the bare invocation expects a TTY for its interactive REPL and exits
// non-zero in non-interactive environments. `codex exec` is the
// headless one-shot mode.
//
// See spec §7.1, §7.2.
package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/transport"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Errors returned by Run.
var (
	ErrCodexCLINotFound  = errors.New("capture: codex CLI not found on PATH")
	ErrCodexSpawnTimeout = errors.New("capture: codex CLI did not connect within timeout")
	ErrCodexExitNonZero  = errors.New("capture: codex CLI exited non-zero")
	ErrUpstreamForward   = errors.New("capture: trivial forward to codex.com failed")
	ErrCaptureBodyParse  = errors.New("capture: codex CLI request body was not valid JSON")
)

// Result is the captured per-session identity material.
type Result struct {
	HeaderBundle   http.Header
	ServiceTier    string
	InstallationID string
	SessionID      string
	RawBody        []byte
}

// Options configures Run.
type Options struct {
	Cred        *store.Credential
	Transport   *transport.Transport
	Stdout      io.Writer
	Stderr      io.Writer
	Timeout     time.Duration // default 30s
	UpstreamURL string        // test override; defaults to "https://chatgpt.com"
}

// codexBody is the expected shape of the JSON body codex CLI sends.
type codexBody struct {
	ServiceTier    string `json:"service_tier"`
	SessionID      string `json:"session_id"`
	ClientMetadata struct {
		InstallationID string `json:"x-codex-installation-id"`
	} `json:"client_metadata"`
}

// Run spins up the local listener, spawns codex, captures its request,
// forwards it to the upstream, waits for codex to exit, and returns the
// captured identity bundle.
//
// The function returns only after BOTH (a) the request was captured AND
// (b) codex CLI exited. If either step fails, the process is killed and
// an appropriate error is returned.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	upstreamURL := opts.UpstreamURL
	if upstreamURL == "" {
		upstreamURL = "https://chatgpt.com"
	}

	// Step 1: locate codex CLI.
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCodexCLINotFound, err)
	}

	// captureCh receives a captureEvent from the mux handler.
	// Buffered so the handler never blocks even if Run already timed out.
	type captureEvent struct {
		result *Result
		err    error
	}
	captureCh := make(chan captureEvent, 1)

	// Step 2: start the local capture server.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		// Read and stash the body so we can forward it AND parse it.
		rawBody, readErr := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if readErr != nil {
			// Defensive: io.ReadAll on an http.Request body rarely fails
			// (it's an in-process buffer); a client disconnect mid-send would
			// cause this. Untestable via normal curl stubs but kept for safety.
			captureCh <- captureEvent{err: fmt.Errorf("%w: read body: %v", ErrCaptureBodyParse, readErr)}
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}

		// Parse body fields.
		var cb codexBody
		if jsonErr := json.Unmarshal(rawBody, &cb); jsonErr != nil {
			captureCh <- captureEvent{err: fmt.Errorf("%w: %v", ErrCaptureBodyParse, jsonErr)}
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// Clone headers — r.Header will be GC'd after the handler returns.
		bundle := r.Header.Clone()

		// Forward to upstream using the bogdanfinn transport.
		fwdURL := upstreamURL + "/v1/responses"
		fwdReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, fwdURL, bytes.NewReader(rawBody))
		if reqErr != nil {
			// untestable: method and URL are controlled constants; NewRequestWithContext
			// only fails on invalid method or malformed URL, neither of which apply here.
			captureCh <- captureEvent{err: fmt.Errorf("%w: build forward request: %v", ErrUpstreamForward, reqErr)}
			http.Error(w, "build fwd req", http.StatusInternalServerError)
			return
		}
		// Carry inbound headers to upstream.
		for k, vs := range bundle {
			for _, v := range vs {
				fwdReq.Header.Add(k, v)
			}
		}
		// Inject bearer from credential.
		if opts.Cred != nil {
			if tok := opts.Cred.AccessToken(); tok != "" {
				fwdReq.Header.Set("Authorization", "Bearer "+tok)
			}
		}

		var fwdResp *http.Response
		if opts.Transport != nil {
			fwdResp, err = opts.Transport.Do(fwdReq)
		} else {
			fwdResp, err = http.DefaultClient.Do(fwdReq) //nolint:bodyclose
		}
		if err != nil || fwdResp == nil || fwdResp.StatusCode >= 400 {
			if fwdResp != nil {
				_ = fwdResp.Body.Close()
			}
			fwdErr := fmt.Errorf("%w: upstream status or network error", ErrUpstreamForward)
			if err != nil {
				fwdErr = fmt.Errorf("%w: %v", ErrUpstreamForward, err)
			}
			captureCh <- captureEvent{err: fwdErr}
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		defer fwdResp.Body.Close()

		// Relay upstream response headers and body back to codex CLI so it
		// exits cleanly.
		for k, vs := range fwdResp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(fwdResp.StatusCode)
		_, _ = io.Copy(w, fwdResp.Body)

		// Signal successful capture.
		res := &Result{
			HeaderBundle:   bundle,
			ServiceTier:    cb.ServiceTier,
			InstallationID: cb.ClientMetadata.InstallationID,
			SessionID:      cb.SessionID,
			RawBody:        rawBody,
		}
		captureCh <- captureEvent{result: res}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Step 3: spawn codex CLI.
	tctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	localURL := srv.URL + "/v1"
	// `codex exec` is the headless invocation; bare `codex "prompt"` requires
	// a TTY (stdin must be a terminal) and exits non-zero in non-interactive
	// environments — including ours, since we capture in a child process
	// without a controlling terminal.
	//nolint:gosec // codexBin comes from exec.LookPath; args are controlled.
	cmd := exec.Command(codexBin, "exec", "say hi", "--config", "openai_base_url="+localURL)
	// setSysProcAttr sets Setpgid=true on Unix so we can kill the entire
	// process group (including children like curl or sleep spawned by shell
	// stubs). This is a no-op on Windows (see capture_windows.go).
	setSysProcAttr(cmd)
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	}
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}

	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrCodexSpawnTimeout, startErr)
	}

	// killProc terminates the process group on Unix (PGID = PID of the group
	// leader), or just the process on Windows. The platform-specific helper
	// killProcessGroup is defined in capture_unix.go / capture_windows.go.
	killProc := func() {
		killProcessGroup(cmd)
	}

	// Step 4: wait for either capture or timeout, then wait for codex to exit.
	//
	// exitCh carries the exit error (nil on clean exit) so we can distinguish
	// between "codex exited non-zero" and "upstream forward failed".
	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()

	// Wait for a capture event.
	var captured captureEvent
	select {
	case captured = <-captureCh:
		// Capture fired (success or error). Fall through to wait for exit.
	case exitErr := <-exitCh:
		// codex CLI exited before it ever made a request.
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			// untestable: requires the deadline to fire in the exact nanosecond
			// window between the exitCh select case winning and this check.
			// The tctx.Done() case above handles the standard timeout path.
			return nil, ErrCodexSpawnTimeout
		}
		if exitErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrCodexExitNonZero, exitErr)
		}
		// Exited cleanly without a capture — treat as timeout.
		return nil, ErrCodexSpawnTimeout
	case <-tctx.Done():
		killProc()
		<-exitCh
		return nil, ErrCodexSpawnTimeout
	}

	// Capture fired. If the capture itself was an error (upstream/parse),
	// wait a moment for the process to finish but don't block the error return.
	if captured.err != nil {
		select {
		case <-exitCh:
		case <-tctx.Done():
			killProc()
			<-exitCh
		}
		return nil, captured.err
	}

	// Happy path: wait for codex to exit cleanly.
	select {
	case exitErr := <-exitCh:
		if exitErr != nil {
			if errors.Is(tctx.Err(), context.DeadlineExceeded) {
				// untestable: the deadline fires in the nanosecond window between
				// exitCh receiving and this check. The tctx.Done() case handles
				// the normal timeout path.
				return nil, ErrCodexSpawnTimeout
			}
			return nil, fmt.Errorf("%w: %v", ErrCodexExitNonZero, exitErr)
		}
		return captured.result, nil
	case <-tctx.Done():
		killProc()
		<-exitCh
		return nil, ErrCodexSpawnTimeout
	}
}
