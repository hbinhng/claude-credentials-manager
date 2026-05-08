package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
)

// modelCmd is the parent group. New subcommands (e.g. `model list`) can
// hang off it later; for now `discovery` is the only child.
var modelCmd = &cobra.Command{
	Use:   "model",
	Short: "Inspect model name resolution",
	Long: `model groups subcommands that inspect how Claude Code resolves
` + "`--model`" + ` arguments to the model strings it actually sends to the
API. Useful when authoring ` + "`--model-alias`" + ` patterns for ` + "`ccm share`" + ` /
` + "`ccm launch`" + ` against codex credentials.`,
}

var modelDiscoveryFrom string

var modelDiscoveryCmd = &cobra.Command{
	Use:   "discovery",
	Short: "Discover what model string Claude Code sends to the API",
	Long: `Spawns a local intercepting proxy, runs ` + "`claude --model <FROM> -p`" + ` against
it, captures the model field claude actually puts in the API request body,
and prints the result. Returns a synthetic Anthropic SSE response so claude
exits cleanly without burning real API quota — no active credential or
internet required.

Example:
  ccm model discovery --from opus
  → claude --model 'opus' → API model 'claude-opus-4-5-20250929'`,
	RunE: runModelDiscovery,
}

func init() {
	rootCmd.AddCommand(modelCmd)
	modelCmd.AddCommand(modelDiscoveryCmd)
	modelDiscoveryCmd.Flags().StringVar(&modelDiscoveryFrom, "from", "",
		"the --model argument to pass to claude (e.g. opus, sonnet, haiku, or a full model id)")
	_ = modelDiscoveryCmd.MarkFlagRequired("from")
}

// modelDiscoveryClaudeBin is the binary name; overridable for tests.
var modelDiscoveryClaudeBin = "claude"

// modelDiscoveryTimeout caps the total wall-clock duration. Tests
// override to a smaller value via SetModelDiscoveryTimeoutForTest.
var modelDiscoveryTimeout = 30 * time.Second

// SetModelDiscoveryTimeoutForTest swaps the timeout for the duration of
// a test and returns a restore closure. Tests override to keep failing
// scenarios quick.
func SetModelDiscoveryTimeoutForTest(d time.Duration) (restore func()) {
	prev := modelDiscoveryTimeout
	modelDiscoveryTimeout = d
	return func() { modelDiscoveryTimeout = prev }
}

func runModelDiscovery(cmd *cobra.Command, _ []string) error {
	if _, err := exec.LookPath(modelDiscoveryClaudeBin); err != nil {
		return fmt.Errorf("claude CLI not found on PATH. Install Claude Code from "+
			"<https://docs.anthropic.com/claude/docs/claude-code> first: %w", err)
	}

	captured := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var probe struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &probe)
		select {
		case captured <- probe.Model:
		default:
			// Already captured; ignore subsequent requests in this session.
		}
		writeSyntheticAnthropicSSE(w, probe.Model)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	parent := cmd.Context()
	if parent == nil {
		// cobra fills this in via Execute; tests that invoke RunE directly
		// pass nil. Fall back to a fresh background context.
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, modelDiscoveryTimeout)
	defer cancel()

	args := []string{"--model", modelDiscoveryFrom, "-p", "ccm model discovery probe"}
	c := exec.CommandContext(ctx, modelDiscoveryClaudeBin, args...)
	// Keep claude's stderr visible so install/auth issues surface; discard
	// stdout (the synthetic response payload is not interesting to humans).
	c.Stderr = cmd.ErrOrStderr()
	c.Stdout = io.Discard
	c.Env = filterEnv(os.Environ(),
		// Strip variables that would otherwise override our redirect.
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
	)
	c.Env = append(c.Env,
		"ANTHROPIC_BASE_URL="+srv.URL,
		// Dummy bearer; the local proxy never validates it. Setting this
		// avoids claude failing on missing-credentials before it sends the
		// request.
		"ANTHROPIC_API_KEY=ccm-discovery",
	)

	if err := c.Start(); err != nil {
		// Untestable in practice: exec.LookPath above already verified the
		// binary exists and is executable; Start() failures from here would
		// require races (binary deleted between checks) or permission
		// changes — neither reliably reproducible in a unit test.
		return fmt.Errorf("spawn claude: %w", err)
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- c.Wait() }()

	select {
	case model := <-captured:
		// Let claude exit on its own (it will, after the synthetic response).
		<-exitCh
		fmt.Fprintf(cmd.OutOrStdout(), "claude --model %q -> API model %q\n",
			modelDiscoveryFrom, model)
		return nil
	case err := <-exitCh:
		// Claude exited before posting a request. Most likely cause:
		// claude rejected the model arg or hit an auth-prep error.
		if err != nil {
			return fmt.Errorf("claude exited without sending a request: %w", err)
		}
		return errors.New("claude exited without sending a request")
	case <-ctx.Done():
		_ = c.Process.Kill()
		<-exitCh
		return fmt.Errorf("timeout: claude did not send a request within %s", modelDiscoveryTimeout)
	}
}

// writeSyntheticAnthropicSSE emits a minimal complete Anthropic SSE
// response that satisfies Claude Code's stream parser so the child
// process exits cleanly. Mirrors the shape produced by
// internal/codex/translator/stream.go (message_start → text content
// block → message_delta → message_stop).
func writeSyntheticAnthropicSSE(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	emit := func(name, data string) {
		_, _ = io.WriteString(w, "event: "+name+"\n")
		_, _ = io.WriteString(w, "data: "+data+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}

	if model == "" {
		model = "unknown"
	}
	msID := "msg_ccm_discovery"

	emit("message_start", `{"type":"message_start","message":{"id":"`+msID+
		`","type":"message","role":"assistant","model":"`+jsonEscape(model)+
		`","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
	emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`)
	emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
	emit("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
	emit("message_stop", `{"type":"message_stop"}`)
}

// jsonEscape produces a minimally-escaped string suitable for embedding
// inside a JSON string literal. Only quotes and backslashes need
// escaping in our use (model names from claude are restricted to safe
// characters); we keep the helper small rather than pulling in
// json.Marshal for a single field.
func jsonEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}
