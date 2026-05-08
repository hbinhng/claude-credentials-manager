package store_test

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func makeJWTExp(t *testing.T, exp int64) string {
	t.Helper()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(
		`{"exp":` + strconv.FormatInt(exp, 10) + `,"email":"u@x.com"}`,
	))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return h + "." + p + "." + s
}

func TestUnmarshal_CodexFile_PopulatesCodexFields(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	raw := []byte(`{
		"id":"u","name":"n","provider":"codex",
		"createdAt":"t","lastRefreshedAt":"t",
		"auth_mode":"chatgpt","OPENAI_API_KEY":null,
		"tokens":{"id_token":"` + makeJWTExp(t, exp) + `","access_token":"` + makeJWTExp(t, exp) + `","refresh_token":"rt_a.b","account_id":"acct"},
		"last_refresh":"2026-05-08T00:00:00.000000Z"
	}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	if c.ProviderName() != "codex" { t.Fatalf("provider mismatch") }
	if c.AuthMode != "chatgpt" { t.Fatalf("auth_mode") }
	if c.OpenAIAPIKey != nil { t.Fatalf("OPENAI_API_KEY: want nil") }
	if c.Tokens == nil || c.Tokens.AccountID != "acct" { t.Fatalf("tokens not populated") }
	if c.LastRefresh == "" { t.Fatalf("last_refresh") }
	if c.IsExpired() { t.Fatalf("should not be expired") }
}

func TestMarshal_CodexNeverEmitsClaudeFields(t *testing.T) {
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "codex",
		AuthMode: "chatgpt", OpenAIAPIKey: nil,
		Tokens: &store.CodexTokens{IDToken: "i", AccessToken: "a", RefreshToken: "r", AccountID: "acct"},
		LastRefresh:     "2026-05-08T00:00:00Z",
		CreatedAt:       "2026-05-08T00:00:00Z",
		LastRefreshedAt: "2026-05-08T00:00:00Z",
	}
	b, err := json.Marshal(c)
	if err != nil { t.Fatal(err) }
	for _, banned := range []string{`"claudeAiOauth"`, `"subscription"`} {
		if strings.Contains(string(b), banned) {
			t.Fatalf("codex credential JSON must not contain %s; got %s", banned, b)
		}
	}
	if !strings.Contains(string(b), `"OPENAI_API_KEY":null`) {
		t.Fatalf("codex must always emit OPENAI_API_KEY:null; got %s", b)
	}
}

func TestUnmarshal_UnknownProvider_Errors(t *testing.T) {
	raw := []byte(`{"id":"x","name":"n","provider":"gemini","createdAt":"t","lastRefreshedAt":"t"}`)
	var c store.Credential
	err := json.Unmarshal(raw, &c)
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("want unknown provider error; got %v", err)
	}
}

func TestIsExpired_Codex_TreatsCorruptJWTAsExpired(t *testing.T) {
	raw := []byte(`{
		"id":"x","name":"n","provider":"codex",
		"createdAt":"t","lastRefreshedAt":"t",
		"auth_mode":"chatgpt","OPENAI_API_KEY":null,
		"tokens":{"id_token":"junk","access_token":"junk","refresh_token":"r","account_id":"a"},
		"last_refresh":"t"
	}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	if !c.IsExpired() { t.Fatalf("corrupt JWT should be treated as expired") }
}

func TestUnmarshal_CachesExpiresAtMillisOnLoad_Codex(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour).Unix()
	raw := []byte(`{
		"id":"x","name":"n","provider":"codex",
		"createdAt":"t","lastRefreshedAt":"t",
		"auth_mode":"chatgpt","OPENAI_API_KEY":null,
		"tokens":{"id_token":"` + makeJWTExp(t, exp) + `","access_token":"` + makeJWTExp(t, exp) + `","refresh_token":"r","account_id":"a"},
		"last_refresh":"t"
	}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	if c.IsExpired() { t.Fatalf("should not be expired") }
}

func TestUnmarshal_CachesExpiresAtMillisOnLoad_Claude(t *testing.T) {
	future := time.Now().Add(time.Hour).UnixMilli()
	raw := []byte(`{"id":"x","name":"n","claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":` + strconv.FormatInt(future, 10) + `,"scopes":["s"]},"subscription":{"tier":"max"},"createdAt":"t","lastRefreshedAt":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	if c.IsExpired() { t.Fatal("should not be expired") }
}

// ---------------------------------------------------------------------------
// Error-path coverage for credential.go
// ---------------------------------------------------------------------------

func TestUnmarshal_Credential_GarbageJSON_Errors(t *testing.T) {
	var c store.Credential
	if err := json.Unmarshal([]byte("not json"), &c); err == nil {
		t.Fatal("expected error on garbage input")
	}
}

func TestUnmarshal_Credential_BadStringField_Errors(t *testing.T) {
	// "id" must be a string, not a number.
	raw := []byte(`{"id":42,"name":"n","createdAt":"t","lastRefreshedAt":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when id is not a string")
	}
}

func TestUnmarshal_Credential_BadClaudeAiOauth_Errors(t *testing.T) {
	// claudeAiOauth must be an object, not a string.
	raw := []byte(`{"id":"x","name":"n","createdAt":"t","lastRefreshedAt":"t","claudeAiOauth":"bad","subscription":{"tier":"max"}}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when claudeAiOauth is invalid")
	}
}

func TestUnmarshal_Credential_BadSubscription_Errors(t *testing.T) {
	// subscription must be an object, not a number.
	raw := []byte(`{"id":"x","name":"n","createdAt":"t","lastRefreshedAt":"t","claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":1,"scopes":["s"]},"subscription":42}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when subscription is invalid")
	}
}

func TestUnmarshal_Credential_CodexBadAuthMode_Errors(t *testing.T) {
	// auth_mode must be a string, not a number.
	raw := []byte(`{"id":"x","name":"n","provider":"codex","createdAt":"t","lastRefreshedAt":"t","auth_mode":99,"OPENAI_API_KEY":null,"tokens":{},"last_refresh":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when auth_mode is not a string")
	}
}

func TestUnmarshal_Credential_CodexBadTokens_Errors(t *testing.T) {
	// tokens must be an object, not a string.
	raw := []byte(`{"id":"x","name":"n","provider":"codex","createdAt":"t","lastRefreshedAt":"t","auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":"bad","last_refresh":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when tokens is not an object")
	}
}

func TestUnmarshal_Credential_CodexOpenAIAPIKeyPresent(t *testing.T) {
	// OPENAI_API_KEY with a real string value (apikey mode).
	apiKey := "sk-test-key"
	raw := []byte(`{"id":"x","name":"n","provider":"codex","createdAt":"t","lastRefreshedAt":"t","auth_mode":"apikey","OPENAI_API_KEY":"` + apiKey + `","tokens":{"id_token":"i","access_token":"a","refresh_token":"r","account_id":"acct"},"last_refresh":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	if c.OpenAIAPIKey == nil || *c.OpenAIAPIKey != apiKey {
		t.Fatalf("OpenAIAPIKey: want %q, got %v", apiKey, c.OpenAIAPIKey)
	}
}

func TestMarshal_Credential_CodexWithAPIKey(t *testing.T) {
	key := "sk-real-key"
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "codex",
		AuthMode: "apikey", OpenAIAPIKey: &key,
		Tokens: &store.CodexTokens{IDToken: "i", AccessToken: "a", RefreshToken: "r", AccountID: "acct"},
		LastRefresh:     "2026-05-08T00:00:00Z",
		CreatedAt:       "2026-05-08T00:00:00Z",
		LastRefreshedAt: "2026-05-08T00:00:00Z",
	}
	b, err := json.Marshal(c)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(b), `"OPENAI_API_KEY":"sk-real-key"`) {
		t.Fatalf("expected OPENAI_API_KEY with value; got %s", b)
	}
}

func TestMarshal_Credential_CodexNilTokens_EmitsEmptyObject(t *testing.T) {
	// When Tokens is nil, marshal should emit an empty CodexTokens object.
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "codex",
		AuthMode: "chatgpt", OpenAIAPIKey: nil, Tokens: nil,
		LastRefresh:     "t",
		CreatedAt:       "t",
		LastRefreshedAt: "t",
	}
	b, err := json.Marshal(c)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(b), `"tokens"`) {
		t.Fatalf("expected tokens key in output; got %s", b)
	}
}

func TestMarshal_Credential_UnknownProvider_Errors(t *testing.T) {
	c := &store.Credential{
		ID: "x", Name: "n", Provider: "gemini",
		CreatedAt: "t", LastRefreshedAt: "t",
	}
	_, err := json.Marshal(c)
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected unknown provider marshal error; got %v", err)
	}
}

func TestOAuthTokens_UnmarshalJSON_MalformedFields(t *testing.T) {
	// refreshToken must be a string.
	bad := []byte(`{"accessToken":"a","refreshToken":42,"expiresAt":1,"scopes":["s"]}`)
	var tok store.OAuthTokens
	if err := json.Unmarshal(bad, &tok); err == nil {
		t.Error("expected error on bad refreshToken type")
	}
}

func TestOAuthTokens_UnmarshalJSON_MalformedExpiresAt(t *testing.T) {
	bad := []byte(`{"accessToken":"a","refreshToken":"r","expiresAt":"not-a-number","scopes":["s"]}`)
	var tok store.OAuthTokens
	if err := json.Unmarshal(bad, &tok); err == nil {
		t.Error("expected error on bad expiresAt type")
	}
}

func TestOAuthTokens_UnmarshalJSON_MalformedScopes(t *testing.T) {
	bad := []byte(`{"accessToken":"a","refreshToken":"r","expiresAt":1,"scopes":"not-array"}`)
	var tok store.OAuthTokens
	if err := json.Unmarshal(bad, &tok); err == nil {
		t.Error("expected error on bad scopes type")
	}
}

func TestParseJWTExpMillis_InvalidBase64(t *testing.T) {
	// Second part is not valid base64url — should return 0.
	token := "header.!!!invalid!!!.sig"
	// indirectly test via codex unmarshal with corrupt access_token
	raw := []byte(`{
		"id":"x","name":"n","provider":"codex",
		"createdAt":"t","lastRefreshedAt":"t",
		"auth_mode":"chatgpt","OPENAI_API_KEY":null,
		"tokens":{"id_token":"i","access_token":"` + token + `","refresh_token":"r","account_id":"a"},
		"last_refresh":"t"
	}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	// Invalid base64 → expiresAtMillis=0 → expired
	if !c.IsExpired() { t.Fatalf("invalid base64 JWT should be treated as expired") }
}

func TestUnmarshal_Credential_BadNameField_Errors(t *testing.T) {
	raw := []byte(`{"id":"x","name":42,"createdAt":"t","lastRefreshedAt":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when name is not a string")
	}
}

func TestUnmarshal_Credential_BadProviderField_Errors(t *testing.T) {
	raw := []byte(`{"id":"x","name":"n","provider":99,"createdAt":"t","lastRefreshedAt":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when provider is not a string")
	}
}

func TestUnmarshal_Credential_BadCreatedAt_Errors(t *testing.T) {
	raw := []byte(`{"id":"x","name":"n","createdAt":true,"lastRefreshedAt":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when createdAt is not a string")
	}
}

func TestUnmarshal_Credential_BadLastRefreshedAt_Errors(t *testing.T) {
	raw := []byte(`{"id":"x","name":"n","createdAt":"t","lastRefreshedAt":99}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when lastRefreshedAt is not a string")
	}
}

func TestUnmarshal_Credential_CodexBadLastRefresh_Errors(t *testing.T) {
	raw := []byte(`{"id":"x","name":"n","provider":"codex","createdAt":"t","lastRefreshedAt":"t","auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":null,"last_refresh":42}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when last_refresh is not a string")
	}
}

func TestUnmarshal_Credential_CodexNoTokens_IsExpired(t *testing.T) {
	// When tokens key is absent, Tokens is nil and expiresAtMillis stays 0 → expired.
	raw := []byte(`{"id":"x","name":"n","provider":"codex","createdAt":"t","lastRefreshedAt":"t","auth_mode":"chatgpt","OPENAI_API_KEY":null,"last_refresh":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	if c.Tokens != nil { t.Fatalf("expected nil Tokens") }
	if !c.IsExpired() { t.Fatalf("codex with no tokens should be expired") }
}

func TestUnmarshal_Credential_RawGarbage_Errors(t *testing.T) {
	// This exercises the json.Unmarshal(data, &raw) error path at the top of
	// Credential.UnmarshalJSON. Previous coverage showed this branch was missed
	// because encoding/json may short-circuit before calling UnmarshalJSON.
	// Wrapping in a struct forces the custom unmarshaler to be called.
	type wrapper struct {
		C store.Credential `json:"c"`
	}
	var w wrapper
	// A JSON object with "c" pointing to a non-object value (array) triggers
	// an unmarshal error inside Credential.UnmarshalJSON.
	if err := json.Unmarshal([]byte(`{"c":[1,2,3]}`), &w); err == nil {
		t.Fatal("expected error when credential JSON is an array, not an object")
	}
}

func TestUnmarshal_Credential_CodexBadOpenAIAPIKey_Errors(t *testing.T) {
	// OPENAI_API_KEY is present, not null, but is not a valid string (a number).
	// This exercises the error path inside the OPENAI_API_KEY parsing block.
	raw := []byte(`{"id":"x","name":"n","provider":"codex","createdAt":"t","lastRefreshedAt":"t","auth_mode":"chatgpt","OPENAI_API_KEY":42,"tokens":{},"last_refresh":"t"}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err == nil {
		t.Fatal("expected error when OPENAI_API_KEY is a number, not a string or null")
	}
}

func TestParseJWTExpMillis_BadPayloadJSON(t *testing.T) {
	// Payload is valid base64 but not valid JSON.
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(`not json`))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	token := h + "." + p + "." + s
	raw := []byte(`{
		"id":"x","name":"n","provider":"codex",
		"createdAt":"t","lastRefreshedAt":"t",
		"auth_mode":"chatgpt","OPENAI_API_KEY":null,
		"tokens":{"id_token":"i","access_token":"` + token + `","refresh_token":"r","account_id":"a"},
		"last_refresh":"t"
	}`)
	var c store.Credential
	if err := json.Unmarshal(raw, &c); err != nil { t.Fatal(err) }
	if !c.IsExpired() { t.Fatalf("bad payload JSON should be treated as expired") }
}
