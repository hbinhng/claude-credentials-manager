package middleware

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReadGrokVersion_FromFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grok", "version.json"),
		[]byte(`{"version":"9.9.9","stable_version":"9.9.8"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != "9.9.9" {
		t.Fatalf("readGrokVersion = %q, want 9.9.9", got)
	}
}

func TestReadGrokVersion_FallbackWhenAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no .grok
	if got := readGrokVersion(); got != defaultGrokClientVersion {
		t.Fatalf("readGrokVersion = %q, want default %q", got, defaultGrokClientVersion)
	}
}

func TestReadGrokVersion_FallbackOnBadJSON(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".grok", "version.json"), []byte(`not json`), 0o644)
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != defaultGrokClientVersion {
		t.Fatalf("bad json → want default, got %q", got)
	}
}

func TestReadGrokVersion_FallbackToStableVersion(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".grok", "version.json"), []byte(`{"stable_version":"3.2.1"}`), 0o644)
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != "3.2.1" {
		t.Fatalf("want stable_version 3.2.1, got %q", got)
	}
}

func TestApplyGrokIdentity_ConstantHeaders(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://cli-chat-proxy.grok.com/v1/messages", nil)
	applyGrokIdentity(req, "grok-4.5", "sess-1", 1, true)

	if !strings.HasPrefix(req.Header.Get("User-Agent"), "grok-shell/") {
		t.Errorf("User-Agent = %q, want grok-shell/ prefix", req.Header.Get("User-Agent"))
	}
	checks := map[string]string{
		"x-xai-token-auth":         "xai-grok-cli",
		"x-grok-client-identifier": "grok-shell",
		"x-grok-client-mode":       "headless",
		"x-authenticateresponse":   "authenticate-response",
		"x-compaction-at":          "400000",
		"Content-Type":             "application/json",
		"Accept-Encoding":          "gzip, br, deflate",
		"Accept":                   "text/event-stream",
		"x-grok-model-override":    "grok-4.5",
	}
	for k, want := range checks {
		if got := req.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if req.Header.Get("x-grok-client-version") == "" {
		t.Error("x-grok-client-version empty")
	}
	// Authorization is the caller's responsibility — identity must not set it.
	if req.Header.Get("Authorization") != "" {
		t.Error("applyGrokIdentity must not set Authorization")
	}
}

func TestApplyGrokIdentity_AcceptFollowsStream(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x/v1/messages", nil)
	applyGrokIdentity(req, "m", "s", 1, false)
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("non-stream Accept = %q, want application/json", got)
	}
}

func TestApplyGrokIdentity_SessionScoped(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x/v1/messages", nil)
	applyGrokIdentity(req, "m", "sess-abc", 2, true)
	if req.Header.Get("x-grok-conv-id") != "sess-abc" || req.Header.Get("x-grok-session-id") != "sess-abc" {
		t.Errorf("conv/session id should equal the session id")
	}
	if req.Header.Get("x-grok-agent-id") == "" {
		t.Error("agent-id should be set when a session id is present")
	}
	if req.Header.Get("x-grok-turn-idx") != "2" {
		t.Errorf("turn-idx = %q, want 2", req.Header.Get("x-grok-turn-idx"))
	}

	// agent-id is deterministic per session.
	req2, _ := http.NewRequest("POST", "https://x/v1/messages", nil)
	applyGrokIdentity(req2, "m", "sess-abc", 5, true)
	if req.Header.Get("x-grok-agent-id") != req2.Header.Get("x-grok-agent-id") {
		t.Error("agent-id must be stable for the same session id")
	}
}

func TestApplyGrokIdentity_EmptySessionOmits(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x/v1/messages", nil)
	applyGrokIdentity(req, "m", "", 1, true)
	for _, h := range []string{"x-grok-conv-id", "x-grok-session-id", "x-grok-agent-id"} {
		if req.Header.Get(h) != "" {
			t.Errorf("%s should be omitted when session id is empty", h)
		}
	}
}

func TestApplyGrokIdentity_PerRequestFresh(t *testing.T) {
	tp := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	req1, _ := http.NewRequest("POST", "https://x/v1/messages", nil)
	req2, _ := http.NewRequest("POST", "https://x/v1/messages", nil)
	applyGrokIdentity(req1, "m", "s", 1, true)
	applyGrokIdentity(req2, "m", "s", 1, true)

	if !tp.MatchString(req1.Header.Get("traceparent")) {
		t.Errorf("traceparent %q not W3C-shaped", req1.Header.Get("traceparent"))
	}
	if req1.Header.Get("traceparent") == req2.Header.Get("traceparent") {
		t.Error("traceparent must be fresh per request")
	}
	if req1.Header.Get("x-grok-req-id") == "" || req1.Header.Get("x-grok-req-id") == req2.Header.Get("x-grok-req-id") {
		t.Error("x-grok-req-id must be fresh per request")
	}
}
