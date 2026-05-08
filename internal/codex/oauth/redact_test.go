package codexoauth_test

import (
	"strings"
	"testing"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

func TestRedact_JWT_3Segment(t *testing.T) {
	in := "auth failed: eyJhbGciOiJSUzI1NiIs.eyJleHAiOjE.signaturepart and rest"
	out := codexoauth.Redact(in)
	if strings.Contains(out, "eyJ") {
		t.Fatalf("JWT not redacted: %s", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Fatalf("no replacement marker: %s", out)
	}
}

func TestRedact_RotatingRefreshToken(t *testing.T) {
	in := "body: {refresh_token: rt_abc-DEF_123.xyz-ABC_789}"
	out := codexoauth.Redact(in)
	if strings.Contains(out, "rt_abc") {
		t.Fatalf("rt_ token not redacted: %s", out)
	}
}

func TestRedact_BearerHeader(t *testing.T) {
	in := "Authorization: Bearer sk-abc.def-XYZ_123 and more"
	out := codexoauth.Redact(in)
	if strings.Contains(out, "sk-abc") {
		t.Fatalf("bearer not redacted: %s", out)
	}
}

func TestRedact_JSONTokenField_AccessToken(t *testing.T) {
	in := `{"access_token": "abc.def.ghi", "x":"y"}`
	out := codexoauth.Redact(in)
	if strings.Contains(out, "abc.def.ghi") {
		t.Fatalf("access_token value not redacted: %s", out)
	}
	if !strings.Contains(out, `"x":"y"`) {
		t.Fatalf("non-token field corrupted: %s", out)
	}
}

func TestRedact_JSONTokenField_RefreshToken(t *testing.T) {
	in := `{"refresh_token":"rt_abc.def"}`
	out := codexoauth.Redact(in)
	if strings.Contains(out, "rt_abc.def") {
		t.Fatalf("refresh_token value not redacted: %s", out)
	}
}

func TestRedact_JSONTokenField_IDToken(t *testing.T) {
	in := `{"id_token": "eyJa.eyJb.sig"}`
	out := codexoauth.Redact(in)
	if strings.Contains(out, "eyJa.eyJb") {
		t.Fatalf("id_token value not redacted: %s", out)
	}
}

func TestRedact_NoTokensIsIdentity(t *testing.T) {
	in := "regular log line with no secrets"
	out := codexoauth.Redact(in)
	if out != in {
		t.Fatalf("redact altered clean string: %q -> %q", in, out)
	}
}
