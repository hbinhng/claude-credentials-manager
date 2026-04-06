package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// config.go tests
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	t.Parallel()

	t.Run("ClientID", func(t *testing.T) {
		t.Parallel()
		if ClientID != "9d1c250a-e61b-44d9-88ed-5944d1962f5e" {
			t.Fatalf("unexpected ClientID: %s", ClientID)
		}
	})

	t.Run("AuthorizeURL", func(t *testing.T) {
		t.Parallel()
		if AuthorizeURL != "https://claude.ai/oauth/authorize" {
			t.Fatalf("unexpected AuthorizeURL: %s", AuthorizeURL)
		}
	})

	t.Run("RedirectURI", func(t *testing.T) {
		t.Parallel()
		if RedirectURI != "https://platform.claude.com/oauth/code/callback" {
			t.Fatalf("unexpected RedirectURI: %s", RedirectURI)
		}
	})

	t.Run("TokenURL default", func(t *testing.T) {
		t.Parallel()
		const expected = "https://console.anthropic.com/v1/oauth/token"
		if TokenURL != expected {
			t.Logf("TokenURL is %q (may have been overridden); expected default %q", TokenURL, expected)
		}
	})

	t.Run("CodeChallengeMethod", func(t *testing.T) {
		t.Parallel()
		if CodeChallengeMethod != "S256" {
			t.Fatalf("unexpected CodeChallengeMethod: %s", CodeChallengeMethod)
		}
	})
}

func TestScopes(t *testing.T) {
	t.Parallel()

	expected := []string{
		"user:inference",
		"user:profile",
		"user:sessions:claude_code",
		"user:mcp_servers",
	}

	if len(Scopes) != len(expected) {
		t.Fatalf("Scopes length = %d, want %d", len(Scopes), len(expected))
	}
	for i, s := range expected {
		if Scopes[i] != s {
			t.Errorf("Scopes[%d] = %q, want %q", i, Scopes[i], s)
		}
	}
}

// ---------------------------------------------------------------------------
// pkce.go tests
// ---------------------------------------------------------------------------

func TestGeneratePKCE_NonEmpty(t *testing.T) {
	t.Parallel()

	p, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error: %v", err)
	}
	if p.CodeVerifier == "" {
		t.Error("CodeVerifier is empty")
	}
	if p.CodeChallenge == "" {
		t.Error("CodeChallenge is empty")
	}
	if p.State == "" {
		t.Error("State is empty")
	}
}

func TestGeneratePKCE_VerifierIsBase64URL(t *testing.T) {
	t.Parallel()

	p, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error: %v", err)
	}

	v := p.CodeVerifier
	if strings.ContainsAny(v, "+/=") {
		t.Errorf("CodeVerifier contains forbidden base64 chars: %s", v)
	}
	if _, err := base64.RawURLEncoding.DecodeString(v); err != nil {
		t.Errorf("CodeVerifier is not valid base64url: %v", err)
	}
}

func TestGeneratePKCE_ChallengeIsSHA256OfVerifier(t *testing.T) {
	t.Parallel()

	p, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error: %v", err)
	}

	hash := sha256.Sum256([]byte(p.CodeVerifier))
	want := base64.RawURLEncoding.EncodeToString(hash[:])
	if p.CodeChallenge != want {
		t.Errorf("CodeChallenge = %q, want SHA256(verifier) = %q", p.CodeChallenge, want)
	}
}

func TestGeneratePKCE_StateDiffersFromVerifier(t *testing.T) {
	t.Parallel()

	p, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error: %v", err)
	}
	if p.State == p.CodeVerifier {
		t.Error("State should differ from CodeVerifier")
	}
}

func TestGeneratePKCE_Randomness(t *testing.T) {
	t.Parallel()

	a, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("first GeneratePKCE() error: %v", err)
	}
	b, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("second GeneratePKCE() error: %v", err)
	}
	if a.CodeVerifier == b.CodeVerifier {
		t.Error("two calls produced the same CodeVerifier — randomness failure")
	}
	if a.State == b.State {
		t.Error("two calls produced the same State — randomness failure")
	}
	if a.CodeChallenge == b.CodeChallenge {
		t.Error("two calls produced the same CodeChallenge — randomness failure")
	}
}

func TestGeneratePKCE_VerifierLength(t *testing.T) {
	t.Parallel()

	p, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error: %v", err)
	}

	// 32 random bytes → base64url with no padding = ceil(32*4/3) = 43 characters
	if len(p.CodeVerifier) != 43 {
		t.Errorf("CodeVerifier length = %d, want 43 (32 bytes base64url-encoded)", len(p.CodeVerifier))
	}
}

// ---------------------------------------------------------------------------
// flow.go — buildAuthorizeURL tests
// ---------------------------------------------------------------------------

func TestBuildAuthorizeURL(t *testing.T) {
	t.Parallel()

	pkce := &PKCEParams{
		CodeVerifier:  "test-verifier",
		CodeChallenge: "test-challenge",
		State:         "test-state",
	}

	raw := buildAuthorizeURL(pkce)

	parsed, err := parseURL(raw)
	if err != nil {
		t.Fatalf("failed to parse URL: %v", err)
	}

	// Base URL
	if got := parsed.scheme + "://" + parsed.host + parsed.path; got != AuthorizeURL {
		t.Errorf("base URL = %q, want %q", got, AuthorizeURL)
	}

	checks := map[string]string{
		"code":                  "true",
		"client_id":             ClientID,
		"response_type":         "code",
		"redirect_uri":          RedirectURI,
		"scope":                 strings.Join(Scopes, " "),
		"code_challenge":        "test-challenge",
		"code_challenge_method": CodeChallengeMethod,
		"state":                 "test-state",
	}
	for key, want := range checks {
		got := parsed.query(key)
		if got != want {
			t.Errorf("query param %q = %q, want %q", key, got, want)
		}
	}
}

func TestBuildAuthorizeURL_UsesFixedRedirectURI(t *testing.T) {
	t.Parallel()

	pkce := &PKCEParams{
		CodeVerifier:  "v",
		CodeChallenge: "c",
		State:         "s",
	}

	raw := buildAuthorizeURL(pkce)
	parsed, err := parseURL(raw)
	if err != nil {
		t.Fatalf("failed to parse URL: %v", err)
	}

	got := parsed.query("redirect_uri")
	if got != RedirectURI {
		t.Errorf("redirect_uri = %q, want fixed %q", got, RedirectURI)
	}
}

// ---------------------------------------------------------------------------
// flow.go — exchangeCode tests
// ---------------------------------------------------------------------------

func TestExchangeCode_CodeWithoutFragment(t *testing.T) {
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "access-abc",
			RefreshToken: "refresh-xyz",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	pkce := &PKCEParams{
		CodeVerifier:  "my-verifier",
		CodeChallenge: "my-challenge",
		State:         "my-state",
	}

	tok, err := exchangeCode("plain-code", "original-state", pkce)
	if err != nil {
		t.Fatalf("exchangeCode() error: %v", err)
	}

	if receivedBody["code"] != "plain-code" {
		t.Errorf("code = %q, want %q", receivedBody["code"], "plain-code")
	}
	if receivedBody["state"] != "original-state" {
		t.Errorf("state = %q, want %q", receivedBody["state"], "original-state")
	}
	if receivedBody["grant_type"] != "authorization_code" {
		t.Errorf("grant_type = %q, want %q", receivedBody["grant_type"], "authorization_code")
	}
	if receivedBody["client_id"] != ClientID {
		t.Errorf("client_id = %q, want %q", receivedBody["client_id"], ClientID)
	}
	if receivedBody["redirect_uri"] != RedirectURI {
		t.Errorf("redirect_uri = %q, want %q", receivedBody["redirect_uri"], RedirectURI)
	}
	if receivedBody["code_verifier"] != "my-verifier" {
		t.Errorf("code_verifier = %q, want %q", receivedBody["code_verifier"], "my-verifier")
	}

	if tok.AccessToken != "access-abc" {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "access-abc")
	}
	if tok.RefreshToken != "refresh-xyz" {
		t.Errorf("RefreshToken = %q, want %q", tok.RefreshToken, "refresh-xyz")
	}
}

func TestExchangeCode_CodeWithFragment(t *testing.T) {
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "tok",
			RefreshToken: "ref",
			ExpiresIn:    60,
		})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	pkce := &PKCEParams{
		CodeVerifier:  "v",
		CodeChallenge: "c",
		State:         "s",
	}

	_, err := exchangeCode("the-code#fragment-state", "original-state", pkce)
	if err != nil {
		t.Fatalf("exchangeCode() error: %v", err)
	}

	if receivedBody["code"] != "the-code" {
		t.Errorf("code = %q, want %q (part before #)", receivedBody["code"], "the-code")
	}
	if receivedBody["state"] != "fragment-state" {
		t.Errorf("state = %q, want %q (part after #)", receivedBody["state"], "fragment-state")
	}
}

func TestExchangeCode_ContentTypeAndMethod(t *testing.T) {
	var contentType string
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		method = r.Method
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "t"})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	pkce := &PKCEParams{CodeVerifier: "v", CodeChallenge: "c", State: "s"}
	_, err := exchangeCode("code", "state", pkce)
	if err != nil {
		t.Fatalf("exchangeCode() error: %v", err)
	}

	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}
	if method != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", method)
	}
}

func TestExchangeCode_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "invalid_grant")
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	pkce := &PKCEParams{CodeVerifier: "v", CodeChallenge: "c", State: "s"}
	_, err := exchangeCode("code", "state", pkce)
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status 400: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should contain response body: %v", err)
	}
}

func TestExchangeCode_UsesFixedRedirectURI(t *testing.T) {
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "t"})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	pkce := &PKCEParams{CodeVerifier: "v", CodeChallenge: "c", State: "s"}
	_, err := exchangeCode("code", "state", pkce)
	if err != nil {
		t.Fatalf("exchangeCode() error: %v", err)
	}

	if receivedBody["redirect_uri"] != RedirectURI {
		t.Errorf("redirect_uri = %q, want fixed %q", receivedBody["redirect_uri"], RedirectURI)
	}
}

// ---------------------------------------------------------------------------
// flow.go — Login tests (copy-code flow)
// ---------------------------------------------------------------------------

func TestLogin_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "access-tok",
			RefreshToken: "refresh-tok",
			ExpiresIn:    7200,
			Scope:        "user:inference user:profile",
		})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	tok, err := Login(func() (string, error) {
		return "my-auth-code\n", nil
	})
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}
	if tok.AccessToken != "access-tok" {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "access-tok")
	}
	if tok.RefreshToken != "refresh-tok" {
		t.Errorf("RefreshToken = %q, want %q", tok.RefreshToken, "refresh-tok")
	}
	if tok.ExpiresIn != 7200 {
		t.Errorf("ExpiresIn = %d, want 7200", tok.ExpiresIn)
	}
}

func TestLogin_EmptyCode(t *testing.T) {
	_, err := Login(func() (string, error) {
		return "  \n", nil
	})
	if err == nil {
		t.Fatal("expected error for empty code, got nil")
	}
	if !strings.Contains(err.Error(), "no code") {
		t.Errorf("error = %v, want to contain 'no code'", err)
	}
}

func TestLogin_ReadError(t *testing.T) {
	_, err := Login(func() (string, error) {
		return "", fmt.Errorf("stdin closed")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "read code") {
		t.Errorf("error = %v, want to contain 'read code'", err)
	}
}

func TestLogin_CodeWithFragment(t *testing.T) {
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "t", ExpiresIn: 60})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	_, err := Login(func() (string, error) {
		return "the-code#some-state\n", nil
	})
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}

	if receivedBody["code"] != "the-code" {
		t.Errorf("code = %q, want %q", receivedBody["code"], "the-code")
	}
	if receivedBody["state"] != "some-state" {
		t.Errorf("state = %q, want %q (fragment after #)", receivedBody["state"], "some-state")
	}
}

func TestLogin_TrimsWhitespace(t *testing.T) {
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "t", ExpiresIn: 60})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	_, err := Login(func() (string, error) {
		return "  my-code  \n", nil
	})
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}

	if receivedBody["code"] != "my-code" {
		t.Errorf("code = %q, want trimmed %q", receivedBody["code"], "my-code")
	}
}

// ---------------------------------------------------------------------------
// refresh.go tests
// ---------------------------------------------------------------------------

func TestRefresh_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresIn:    1800,
			Scope:        "user:inference",
		})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	tok, err := Refresh("old-refresh-token")
	if err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}
	if tok.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "new-access")
	}
	if tok.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken = %q, want %q", tok.RefreshToken, "new-refresh")
	}
	if tok.ExpiresIn != 1800 {
		t.Errorf("ExpiresIn = %d, want 1800", tok.ExpiresIn)
	}
	if tok.Scope != "user:inference" {
		t.Errorf("Scope = %q, want %q", tok.Scope, "user:inference")
	}
}

func TestRefresh_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "invalid_refresh_token")
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	_, err := Refresh("bad-token")
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status 401: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_refresh_token") {
		t.Errorf("error should contain response body: %v", err)
	}
}

func TestRefresh_RequestBody(t *testing.T) {
	var receivedBody map[string]string
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "t"})
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	_, err := Refresh("my-refresh-token")
	if err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	if receivedBody["grant_type"] != "refresh_token" {
		t.Errorf("grant_type = %q, want %q", receivedBody["grant_type"], "refresh_token")
	}
	if receivedBody["refresh_token"] != "my-refresh-token" {
		t.Errorf("refresh_token = %q, want %q", receivedBody["refresh_token"], "my-refresh-token")
	}
	if receivedBody["client_id"] != ClientID {
		t.Errorf("client_id = %q, want %q", receivedBody["client_id"], ClientID)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}
}

func TestRefresh_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "this is not valid json{{{")
	}))
	defer srv.Close()

	origURL := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = origURL }()

	_, err := Refresh("some-token")
	if err == nil {
		t.Fatal("expected error for invalid JSON response, got nil")
	}
	if !strings.Contains(err.Error(), "parse refresh response") {
		t.Errorf("error = %v, want to contain 'parse refresh response'", err)
	}
}

// ---------------------------------------------------------------------------
// usage.go tests
// ---------------------------------------------------------------------------

func TestFetchUsage_FiveHourAndSevenDay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-token")
		}
		if got := r.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Errorf("anthropic-beta = %q, want %q", got, "oauth-2025-04-20")
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"five_hour": {"utilization": 25.5, "resets_at": "2099-01-01T00:00:00Z"},
			"seven_day": {"utilization": 10.0, "resets_at": "2099-01-07T00:00:00Z"}
		}`)
	}))
	defer srv.Close()

	origURL := UsageURL
	UsageURL = srv.URL
	defer func() { UsageURL = origURL }()

	info := FetchUsage("test-token")
	if info.Error != "" {
		t.Fatalf("FetchUsage() error: %s", info.Error)
	}
	if len(info.Quotas) != 2 {
		t.Fatalf("got %d quotas, want 2", len(info.Quotas))
	}

	q5h := info.Quotas[0]
	if q5h.Name != "5h" {
		t.Errorf("quota[0].Name = %q, want %q", q5h.Name, "5h")
	}
	if q5h.Used != 25.5 {
		t.Errorf("quota[0].Used = %f, want 25.5", q5h.Used)
	}
	if q5h.Remaining != 74.5 {
		t.Errorf("quota[0].Remaining = %f, want 74.5", q5h.Remaining)
	}

	q7d := info.Quotas[1]
	if q7d.Name != "7d" {
		t.Errorf("quota[1].Name = %q, want %q", q7d.Name, "7d")
	}
	if q7d.Used != 10.0 {
		t.Errorf("quota[1].Used = %f, want 10.0", q7d.Used)
	}
	if q7d.Remaining != 90.0 {
		t.Errorf("quota[1].Remaining = %f, want 90.0", q7d.Remaining)
	}
}

func TestFetchUsage_ModelSpecificWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"five_hour": {"utilization": 0, "resets_at": "2099-01-01T00:00:00Z"},
			"seven_day": {"utilization": 50, "resets_at": "2099-01-07T00:00:00Z"},
			"seven_day_opus": {"utilization": 80, "resets_at": "2099-01-07T00:00:00Z"},
			"seven_day_sonnet": {"utilization": 15, "resets_at": "2099-01-07T00:00:00Z"}
		}`)
	}))
	defer srv.Close()

	origURL := UsageURL
	UsageURL = srv.URL
	defer func() { UsageURL = origURL }()

	info := FetchUsage("tok")
	if info.Error != "" {
		t.Fatalf("FetchUsage() error: %s", info.Error)
	}

	// 5h + 7d + opus + sonnet = 4
	if len(info.Quotas) != 4 {
		t.Fatalf("got %d quotas, want 4: %+v", len(info.Quotas), info.Quotas)
	}

	// Check model-specific windows exist
	names := map[string]bool{}
	for _, q := range info.Quotas {
		names[q.Name] = true
	}
	if !names["7d/opus"] {
		t.Error("missing 7d/opus quota")
	}
	if !names["7d/sonnet"] {
		t.Error("missing 7d/sonnet quota")
	}
}

func TestFetchUsage_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "unauthorized")
	}))
	defer srv.Close()

	origURL := UsageURL
	UsageURL = srv.URL
	defer func() { UsageURL = origURL }()

	info := FetchUsage("bad-token")
	if info.Error == "" {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(info.Error, "401") {
		t.Errorf("error = %q, want to contain '401'", info.Error)
	}
}

func TestFetchUsage_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	origURL := UsageURL
	UsageURL = srv.URL
	defer func() { UsageURL = origURL }()

	info := FetchUsage("tok")
	if info.Error != "" {
		t.Fatalf("FetchUsage() error: %s", info.Error)
	}
	if len(info.Quotas) != 0 {
		t.Errorf("got %d quotas, want 0", len(info.Quotas))
	}
}

func TestFetchUsage_UtilizationOver100(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"five_hour": {"utilization": 110, "resets_at": "2099-01-01T00:00:00Z"}}`)
	}))
	defer srv.Close()

	origURL := UsageURL
	UsageURL = srv.URL
	defer func() { UsageURL = origURL }()

	info := FetchUsage("tok")
	if info.Error != "" {
		t.Fatalf("FetchUsage() error: %s", info.Error)
	}
	if len(info.Quotas) != 1 {
		t.Fatalf("got %d quotas, want 1", len(info.Quotas))
	}
	if info.Quotas[0].Remaining != 0 {
		t.Errorf("Remaining = %f, want 0 (clamped)", info.Quotas[0].Remaining)
	}
}

func TestFetchUsage_RequestHeaders(t *testing.T) {
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	origURL := UsageURL
	UsageURL = srv.URL
	defer func() { UsageURL = origURL }()

	FetchUsage("my-access-token")

	if got := headers.Get("Authorization"); got != "Bearer my-access-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer my-access-token")
	}
	if got := headers.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q, want %q", got, "oauth-2025-04-20")
	}
	if got := headers.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
	}
}

func TestFormatResetTime_FutureTime(t *testing.T) {
	t.Parallel()
	result := formatResetTime("2099-12-31T23:59:59Z")
	if result == "" || result == "now" {
		t.Errorf("formatResetTime(far future) = %q, want 'in Xh...'", result)
	}
	if !strings.HasPrefix(result, "in ") {
		t.Errorf("formatResetTime(far future) = %q, want prefix 'in '", result)
	}
}

func TestFormatResetTime_PastTime(t *testing.T) {
	t.Parallel()
	result := formatResetTime("2000-01-01T00:00:00Z")
	if result != "now" {
		t.Errorf("formatResetTime(past) = %q, want %q", result, "now")
	}
}

func TestFormatResetTime_Empty(t *testing.T) {
	t.Parallel()
	if got := formatResetTime(""); got != "" {
		t.Errorf("formatResetTime(\"\") = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type parsedURL struct {
	scheme string
	host   string
	path   string
	params map[string]string
}

func (p *parsedURL) query(key string) string {
	return p.params[key]
}

// parseURL is a minimal URL parser that avoids net/url so we test the raw
// string output of buildAuthorizeURL without double-encoding surprises.
func parseURL(raw string) (*parsedURL, error) {
	// Split scheme
	schemeSep := strings.Index(raw, "://")
	if schemeSep < 0 {
		return nil, fmt.Errorf("no scheme in %q", raw)
	}
	scheme := raw[:schemeSep]
	rest := raw[schemeSep+3:]

	// Split host+path from query
	qSep := strings.Index(rest, "?")
	hostPath := rest
	queryStr := ""
	if qSep >= 0 {
		hostPath = rest[:qSep]
		queryStr = rest[qSep+1:]
	}

	// Split host from path
	pathSep := strings.Index(hostPath, "/")
	host := hostPath
	path := ""
	if pathSep >= 0 {
		host = hostPath[:pathSep]
		path = hostPath[pathSep:]
	}

	// Parse query params (simple: split on &, then on =)
	params := map[string]string{}
	if queryStr != "" {
		for _, part := range strings.Split(queryStr, "&") {
			kv := strings.SplitN(part, "=", 2)
			key := decodePercent(kv[0])
			val := ""
			if len(kv) == 2 {
				val = decodePercent(kv[1])
			}
			params[key] = val
		}
	}

	return &parsedURL{scheme: scheme, host: host, path: path, params: params}, nil
}

func decodePercent(s string) string {
	// Use stdlib for percent-decoding
	v, _ := (&strings.Replacer{}).Replace(s), ""
	_ = v
	// Actually just use url.QueryUnescape since it's simpler
	decoded, err := queryUnescape(s)
	if err != nil {
		return s
	}
	return decoded
}

func queryUnescape(s string) (string, error) {
	// Minimal: replace + with space, then %XX
	s = strings.ReplaceAll(s, "+", " ")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi := unhex(s[i+1])
			lo := unhex(s[i+2])
			if hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}
