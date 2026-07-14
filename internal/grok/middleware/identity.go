package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/google/uuid"
)

// defaultGrokClientVersion is the grok-shell version ccm claims when the real
// version can't be read from $HOME/.grok/version.json. Captured from grok-shell
// 0.2.101 (2026-07-14); must stay >= the endpoint's min_client_version (0.1.202).
const defaultGrokClientVersion = "0.2.101"

// grokAgentNamespace is a fixed UUID namespace for deriving a stable per-session
// x-grok-agent-id via UUIDv5. Arbitrary but constant.
var grokAgentNamespace = uuid.MustParse("6b6f7267-0000-0000-0000-000000000001")

var (
	grokVersionOnce sync.Once
	grokVersionVal  string
)

// grokClientVersion returns the grok-shell client version ccm claims, resolved
// best-effort from the local grok-shell install and cached for the process.
func grokClientVersion() string {
	grokVersionOnce.Do(func() { grokVersionVal = readGrokVersion() })
	return grokVersionVal
}

// readGrokVersion reads $HOME/.grok/version.json (grok-shell's own dir, never
// redirected by CCM_HOME) and returns its version, falling back to
// defaultGrokClientVersion on any error.
func readGrokVersion() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultGrokClientVersion
	}
	b, err := os.ReadFile(filepath.Join(home, ".grok", "version.json"))
	if err != nil {
		return defaultGrokClientVersion
	}
	var v struct {
		Version       string `json:"version"`
		StableVersion string `json:"stable_version"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return defaultGrokClientVersion
	}
	if v.Version != "" {
		return v.Version
	}
	if v.StableVersion != "" {
		return v.StableVersion
	}
	return defaultGrokClientVersion
}

// applyGrokIdentity sets the header set grok-shell sends to cli-chat-proxy so
// ccm presents as the official client on the wire. It does NOT set
// Authorization — the caller owns the bearer. model is the target grok model;
// sessionID is the inbound X-Claude-Code-Session-Id ("" when absent); turnIdx
// is a per-session monotonic counter; stream selects the Accept type.
func applyGrokIdentity(req *http.Request, model, sessionID string, turnIdx int, stream bool) {
	ver := grokClientVersion()
	req.Header.Set("User-Agent", fmt.Sprintf("grok-shell/%s (%s; %s)", ver, grokUAOS(), grokUAArch()))
	req.Header.Set("x-xai-token-auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-identifier", "grok-shell")
	req.Header.Set("x-grok-client-version", ver)
	req.Header.Set("x-grok-client-mode", "headless")
	req.Header.Set("x-authenticateresponse", "authenticate-response")
	req.Header.Set("x-compaction-at", "400000")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, br, deflate")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	if sessionID != "" {
		req.Header.Set("x-grok-conv-id", sessionID)
		req.Header.Set("x-grok-session-id", sessionID)
		req.Header.Set("x-grok-agent-id", uuid.NewSHA1(grokAgentNamespace, []byte(sessionID)).String())
	}

	req.Header.Set("x-grok-req-id", uuid.NewString())
	req.Header.Set("traceparent", newTraceparent())
	req.Header.Set("x-grok-turn-idx", fmt.Sprintf("%d", turnIdx))

	if model != "" {
		req.Header.Set("x-grok-model-override", model)
	}
}

// grokUAOS/grokUAArch map Go's GOOS/GOARCH to grok-shell's UA tokens.
func grokUAOS() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

func grokUAArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	default:
		return runtime.GOARCH // arm64, ...
	}
}

// newTraceparent returns a W3C traceparent: 00-<16B trace-id>-<8B span-id>-01.
func newTraceparent() string {
	var traceID [16]byte
	var spanID [8]byte
	_, _ = rand.Read(traceID[:]) // crypto/rand.Read panics on OS failure (Go 1.20+)
	_, _ = rand.Read(spanID[:])
	return "00-" + hex.EncodeToString(traceID[:]) + "-" + hex.EncodeToString(spanID[:]) + "-01"
}
