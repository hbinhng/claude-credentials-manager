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
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// defaultGrokClientVersion is the grok-shell version ccm claims when the real
// version can't be read from $HOME/.grok/version.json, and also the floor: a
// local install older than this is ignored. cli-chat-proxy rejects clients
// below 1.0.13 with HTTP 426 ("Your Grok CLI version (...) is outdated",
// 2026-10-01); 1.0.46 is the version whose wire contract ccm mirrors
// (captured 2026-10-01), so a stale grok-shell install must not drag the
// claimed version below it.
const defaultGrokClientVersion = "1.0.46"

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

// ApplyGrokConstantIdentity sets grok-shell's constant identity headers (UA +
// client identity). Shared by the message terminal (applyGrokIdentity) and the
// quota poller in the grok oauth package, so both present the same client on
// the wire. Does NOT set Authorization — the caller owns the bearer.
func ApplyGrokConstantIdentity(req *http.Request) {
	ver := grokClientVersion()
	req.Header.Set("User-Agent", fmt.Sprintf("grok-shell/%s (%s; %s)", ver, grokUAOS(), grokUAArch()))
	req.Header.Set("x-xai-token-auth", "xai-grok-cli")
	req.Header.Set("x-grok-client-identifier", "grok-shell")
	req.Header.Set("x-grok-client-version", ver)
	req.Header.Set("x-grok-client-mode", "headless")
}

// readGrokVersion reads $HOME/.grok/version.json (grok-shell's own dir, never
// redirected by CCM_HOME) and returns its version, falling back to
// defaultGrokClientVersion on any error or when the local version is older
// than (or not comparable to) that floor.
func readGrokVersion() string {
	if v := readLocalGrokVersion(); versionAtLeast(v, defaultGrokClientVersion) {
		return v
	}
	return defaultGrokClientVersion
}

// readLocalGrokVersion returns the version recorded by the local grok-shell
// install, or "" when it can't be determined.
func readLocalGrokVersion() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".grok", "version.json"))
	if err != nil {
		return ""
	}
	var v struct {
		Version       string `json:"version"`
		StableVersion string `json:"stable_version"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return ""
	}
	if v.Version != "" {
		return v.Version
	}
	if v.StableVersion != "" {
		return v.StableVersion
	}
	return ""
}

// versionAtLeast reports whether dotted version v is >= floor, comparing
// numeric components left to right (missing components count as 0). Any
// pre-release/build suffix ("-beta.1", "+abc") is ignored. An unparseable v
// is never at least the floor.
func versionAtLeast(v, floor string) bool {
	a, ok := parseVersion(v)
	if !ok {
		return false
	}
	b, _ := parseVersion(floor) // floor is a trusted constant
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return true
}

// parseVersion splits a dotted numeric version into its components,
// ignoring any "-pre"/"+build" suffix.
func parseVersion(v string) ([]int, bool) {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// applyGrokIdentity sets the header set grok-shell sends to cli-chat-proxy so
// ccm presents as the official client on the wire. It does NOT set
// Authorization — the caller owns the bearer. model is the target grok model;
// sessionID is the inbound X-Claude-Code-Session-Id ("" when absent); turnIdx
// is a per-session monotonic counter; stream selects the Accept type.
func applyGrokIdentity(req *http.Request, model, sessionID string, turnIdx int, stream bool) {
	ApplyGrokConstantIdentity(req)
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
	_, _ = rand.Read(traceID[:]) // crypto/rand.Read panics on OS failure (Go 1.24+)
	_, _ = rand.Read(spanID[:])
	return "00-" + hex.EncodeToString(traceID[:]) + "-" + hex.EncodeToString(spanID[:]) + "-01"
}
