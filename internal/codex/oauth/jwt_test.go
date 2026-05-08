package codexoauth_test

import (
	"encoding/base64"
	"strings"
	"testing"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

func mkJWT(t *testing.T, payload string) string {
	t.Helper()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(payload))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return h + "." + p + "." + s
}

func TestParseClaims_HappyPath(t *testing.T) {
	tok := mkJWT(t, `{
		"email": "u@example.com",
		"exp": 1900000000,
		"https://api.openai.com/auth": {
			"chatgpt_account_id": "acct-1",
			"chatgpt_plan_type": "pro"
		}
	}`)
	c, err := codexoauth.ParseClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "u@example.com" {
		t.Fatalf("email: %s", c.Email)
	}
	if c.AccountID != "acct-1" {
		t.Fatalf("account_id: %s", c.AccountID)
	}
	if c.PlanType != "pro" {
		t.Fatalf("plan_type: %s", c.PlanType)
	}
	if c.ExpUnixSeconds != 1900000000 {
		t.Fatalf("exp: %d", c.ExpUnixSeconds)
	}
}

func TestParseClaims_MissingNestedAuth_LeavesAccountIDEmpty(t *testing.T) {
	tok := mkJWT(t, `{"email":"u@example.com","exp":1900000000}`)
	c, err := codexoauth.ParseClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccountID != "" {
		t.Fatalf("expected empty AccountID; got %q", c.AccountID)
	}
	if c.PlanType != "" {
		t.Fatalf("expected empty PlanType; got %q", c.PlanType)
	}
}

func TestParseClaims_NotThreeSegments_Errors(t *testing.T) {
	if _, err := codexoauth.ParseClaims("only.two"); err == nil {
		t.Fatal("expected error on 2-segment token")
	}
}

func TestParseClaims_BadBase64_Errors(t *testing.T) {
	if _, err := codexoauth.ParseClaims("aaa.!!!.ccc"); err == nil {
		t.Fatal("expected error on invalid base64 payload")
	}
}

func TestParseClaims_BadJSON_Errors(t *testing.T) {
	bad := mkJWT(t, `{"email":`)
	if _, err := codexoauth.ParseClaims(bad); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestParseClaims_DoesNotVerifySignature(t *testing.T) {
	// Signature is literally "sig" — would fail any real verification.
	tok := mkJWT(t, `{"email":"u@x.com","exp":1900000000}`)
	c, err := codexoauth.ParseClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "u@x.com" {
		t.Fatalf("parser refused unsigned token")
	}
	_ = strings.Split // keep import
}
