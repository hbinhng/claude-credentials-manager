package codexoauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

func TestGeneratePKCE_VerifierLengthInRange(t *testing.T) {
	p, err := codexoauth.GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Verifier) < 43 || len(p.Verifier) > 128 {
		t.Fatalf("verifier length %d out of [43,128]", len(p.Verifier))
	}
}

func TestGeneratePKCE_ChallengeIsS256OfVerifier(t *testing.T) {
	p, err := codexoauth.GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if p.Challenge != want {
		t.Fatalf("challenge mismatch")
	}
}

func TestGeneratePKCE_StateIsURLSafe(t *testing.T) {
	p, err := codexoauth.GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.State) < 16 {
		t.Fatalf("state too short")
	}
	for _, r := range p.State {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			t.Fatalf("state contains non-URL-safe char %q", r)
		}
	}
}

func TestBuildAuthorizeURL_ContainsAllParams(t *testing.T) {
	p := &codexoauth.PKCEParams{Verifier: "v", Challenge: "ch", State: "st"}
	u := codexoauth.BuildAuthorizeURL(p, "http://localhost:1455/auth/callback")
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	want := map[string]string{
		"response_type":              "code",
		"client_id":                  "app_EMoamEEZ73f0CkXaXp7hrann",
		"redirect_uri":               "http://localhost:1455/auth/callback",
		"scope":                      "openid profile email offline_access api.connectors.read api.connectors.invoke",
		"code_challenge":             "ch",
		"code_challenge_method":      "S256",
		"state":                      "st",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"originator":                 "codex_cli_rs",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Fatalf("query %q: got %q, want %q", k, got, v)
		}
	}
	if !strings.HasPrefix(u, "https://auth.openai.com/oauth/authorize?") {
		t.Fatalf("URL prefix wrong: %s", u)
	}
}
