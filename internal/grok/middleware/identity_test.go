package middleware

import (
	"encoding/base64"
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

func TestReadGrokVersion_StaleLocalInstallClampedToFloor(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".grok", "version.json"), []byte(`{"version":"0.2.101"}`), 0o644)
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != defaultGrokClientVersion {
		t.Fatalf("stale local 0.2.101 → want floor %q, got %q", defaultGrokClientVersion, got)
	}
}

func TestReadGrokVersion_NewerLocalInstallWins(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".grok", "version.json"), []byte(`{"version":"1.2.0"}`), 0o644)
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != "1.2.0" {
		t.Fatalf("newer local install → want 1.2.0, got %q", got)
	}
}

func TestReadGrokVersion_JustBelowFloorClamped(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".grok", "version.json"), []byte(`{"version":"1.0.45"}`), 0o644)
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != "1.0.46" {
		t.Fatalf("local 1.0.45 → want floor 1.0.46, got %q", got)
	}
}

func TestReadGrokVersion_UnparseableLocalFallsBackToFloor(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".grok", "version.json"), []byte(`{"version":"nightly"}`), 0o644)
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != defaultGrokClientVersion {
		t.Fatalf("unparseable local → want floor %q, got %q", defaultGrokClientVersion, got)
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v, floor string
		want     bool
	}{
		{"1.0.46", "1.0.46", true},
		{"1.0.47", "1.0.46", true},
		{"1.1.0", "1.0.46", true},
		{"2.0", "1.0.46", true},
		{"1.0.46-beta.1", "1.0.46", true},
		{"1.0.46+abc", "1.0.46", true},
		{"1.0.45", "1.0.46", false},
		{"1.0.13", "1.0.46", false},
		{"0.2.101", "1.0.46", false},
		{"1.0", "1.0.46", false},
		{"nightly", "1.0.46", false},
		{"1.x.3", "1.0.46", false},
		{"1.-1.3", "1.0.46", false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.v, c.floor); got != c.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", c.v, c.floor, got, c.want)
		}
	}
}

// jwtWithSub builds an unsigned JWT-shaped token whose payload has sub.
func jwtWithSub(sub string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"sub":"`+sub+`"}`)) + ".sig"
}

func newIdentityReq(bearer string) *http.Request {
	req, _ := http.NewRequest("POST", "https://cli-chat-proxy.grok.com/v1/responses", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

func TestApplyGrokIdentity_ResponsesHeaders(t *testing.T) {
	req := newIdentityReq(jwtWithSub("user-123"))
	applyGrokIdentity(req, grokRequestIdentity{Model: "grok-4.7", SessionID: "sess-1", CredentialID: "cred-1", TurnIdx: 3})

	if !strings.HasPrefix(req.Header.Get("User-Agent"), "grok-shell/") {
		t.Errorf("User-Agent = %q", req.Header.Get("User-Agent"))
	}
	want := map[string]string{
		"x-xai-token-auth":              "xai-grok-cli",
		"x-grok-client-identifier":      "grok-shell",
		"x-grok-client-mode":            "headless",
		"x-grok-user-id":                "user-123",
		"x-authenticateresponse":        "authenticate-response",
		"x-compaction-at":               "204800",
		"x-compactions-remaining":       "1",
		"x-grok-doom-loop-check":        "1024",
		"x-grok-exact-repetition-check": "64",
		"Content-Type":                  "application/json",
		"Accept":                        "text/event-stream",
		"Accept-Encoding":               "gzip, br, deflate",
		"x-grok-conv-id":                "sess-1",
		"x-grok-session-id":             "sess-1",
		"x-grok-turn-idx":               "3",
		"x-grok-model-override":         "grok-4.7",
	}
	for k, v := range want {
		if got := req.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if req.Header.Get("x-grok-conv-group-id") == "" || req.Header.Get("x-grok-agent-id") == "" {
		t.Error("conv-group-id / agent-id must be set")
	}
	if req.Header.Get("x-grok-conv-group-id") == req.Header.Get("x-grok-agent-id") {
		t.Error("conv-group-id and agent-id must be distinct derivations")
	}
}

func TestApplyGrokIdentity_AgentIDFollowsCredentialNotSession(t *testing.T) {
	a := newIdentityReq("")
	b := newIdentityReq("")
	c := newIdentityReq("")
	applyGrokIdentity(a, grokRequestIdentity{SessionID: "s1", CredentialID: "cred-1"})
	applyGrokIdentity(b, grokRequestIdentity{SessionID: "s2", CredentialID: "cred-1"})
	applyGrokIdentity(c, grokRequestIdentity{SessionID: "s1", CredentialID: "cred-2"})
	if a.Header.Get("x-grok-agent-id") != b.Header.Get("x-grok-agent-id") {
		t.Error("agent-id must be stable across sessions of one credential")
	}
	if a.Header.Get("x-grok-agent-id") == c.Header.Get("x-grok-agent-id") {
		t.Error("agent-id must differ per credential")
	}
	if a.Header.Get("x-grok-conv-group-id") == b.Header.Get("x-grok-conv-group-id") {
		t.Error("conv-group-id must differ per session")
	}
}

func TestApplyGrokIdentity_OmitsWhatItCannotDerive(t *testing.T) {
	req := newIdentityReq("not-a-jwt")
	applyGrokIdentity(req, grokRequestIdentity{})
	for _, h := range []string{"x-grok-user-id", "x-grok-conv-id", "x-grok-session-id", "x-grok-conv-group-id", "x-grok-agent-id", "x-grok-model-override"} {
		if req.Header.Get(h) != "" {
			t.Errorf("%s = %q, want omitted", h, req.Header.Get(h))
		}
	}
	if req.Header.Get("Authorization") != "Bearer not-a-jwt" {
		t.Error("identity must not touch Authorization")
	}
}

func TestApplyGrokIdentity_PerRequestFresh(t *testing.T) {
	tp := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	r1, r2 := newIdentityReq(""), newIdentityReq("")
	applyGrokIdentity(r1, grokRequestIdentity{SessionID: "s"})
	applyGrokIdentity(r2, grokRequestIdentity{SessionID: "s"})
	if !tp.MatchString(r1.Header.Get("traceparent")) || r1.Header.Get("traceparent") == r2.Header.Get("traceparent") {
		t.Error("traceparent must be W3C-shaped and fresh per request")
	}
	if r1.Header.Get("x-grok-req-id") == "" || r1.Header.Get("x-grok-req-id") == r2.Header.Get("x-grok-req-id") {
		t.Error("x-grok-req-id must be fresh per request")
	}
}

func TestBearerSubject(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	for name, tc := range map[string]struct{ bearer, want string }{
		"valid":            {jwtWithSub("u1"), "u1"},
		"padded payload":   {enc([]byte(`{}`)) + "." + base64.URLEncoding.EncodeToString([]byte(`{"sub":"u2"}`)) + ".s", "u2"},
		"no header":        {"", ""},
		"two parts":        {"a.b", ""},
		"bad base64":       {"a.!!!.c", ""},
		"payload not json": {"a." + enc([]byte("nope")) + ".c", ""},
	} {
		if got := bearerSubject(newIdentityReq(tc.bearer)); got != tc.want {
			t.Errorf("%s: bearerSubject = %q, want %q", name, got, tc.want)
		}
	}
}

func TestReadGrokVersion_EmptyVersionFieldsFallBackToFloor(t *testing.T) {
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".grok", "version.json"), []byte(`{}`), 0o644)
	t.Setenv("HOME", home)
	if got := readGrokVersion(); got != defaultGrokClientVersion {
		t.Fatalf("empty version.json → want floor %q, got %q", defaultGrokClientVersion, got)
	}
}

func TestReadGrokVersion_NoHomeFallsBackToFloor(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if got := readGrokVersion(); got != defaultGrokClientVersion {
		t.Fatalf("no home → want floor %q, got %q", defaultGrokClientVersion, got)
	}
}
