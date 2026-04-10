package share

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultCapturePrompt is the user-supplied prompt passed to `claude -p`
// during capture. Something humane and short so anyone watching the
// process list or usage logs sees a normal interaction.
const DefaultCapturePrompt = "just say 'hi'"

// DefaultCaptureTimeout caps how long we wait for claude to produce an
// outbound request. Generous to tolerate a slow first boot.
const DefaultCaptureTimeout = 60 * time.Second

// RunCapture launches `claude -p <prompt>` with ANTHROPIC_BASE_URL set to
// proxyAddr and waits for the proxy to signal that it has captured the
// first request. claude is killed as soon as capture completes so that no
// in-flight request leaks into SERVING mode.
//
// If claude exits before the proxy captures anything, the function
// returns a clear error so the caller can abort the share session.
func RunCapture(ctx context.Context, proxy *Proxy, prompt string) error {
	if prompt == "" {
		prompt = DefaultCapturePrompt
	}

	// Fail fast if the claude binary is not on PATH. The user-facing
	// message here is the one they'll see, so make it actionable.
	if _, err := exec.LookPath("claude"); err != nil {
		return errors.New("could not find 'claude' on PATH — install Claude Code before running `ccm share`")
	}

	runCtx, cancel := context.WithTimeout(ctx, DefaultCaptureTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "claude", "-p", prompt)
	cmd.Env = append(os.Environ(),
		"ANTHROPIC_BASE_URL="+proxy.Addr(),
		// Belt and suspenders: some codepaths also look at this var.
		"ANTHROPIC_API_URL="+proxy.Addr(),
	)
	// Claude's output during capture is noise — a bogus 401 from the
	// proxy will make it print an auth error. Swallow stdout/stderr so
	// the user's terminal stays clean.
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch claude for capture: %w", err)
	}

	// Wait for either the capture signal or for claude to exit early.
	waitC := make(chan error, 1)
	go func() { waitC <- cmd.Wait() }()

	select {
	case <-proxy.CaptureDone():
		// Capture succeeded. Kill claude so its retries don't race the
		// transition into SERVING mode.
		_ = cmd.Process.Signal(os.Interrupt)
		// Give it a brief grace period, then hard-kill.
		select {
		case <-waitC:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-waitC
		}
		return nil

	case err := <-waitC:
		// claude exited before any identity headers reached the proxy.
		if err != nil {
			return fmt.Errorf("claude exited before capture: %w", err)
		}
		return errors.New("claude exited without making a request — is it authenticated? try `claude auth` first")

	case <-runCtx.Done():
		_ = cmd.Process.Kill()
		<-waitC
		return fmt.Errorf("capture timed out after %s", DefaultCaptureTimeout)
	}
}
