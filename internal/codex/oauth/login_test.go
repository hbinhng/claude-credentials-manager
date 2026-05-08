package codexoauth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
	oauth_pkg "github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// mkLoginJWT builds a minimal JWT accepted by the codex JWT parser.
func mkLoginJWT(t *testing.T, email, accountID string) string {
	t.Helper()
	exp := time.Now().Add(time.Hour).Unix()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(
		`{"email":"` + email + `","exp":` + strconv.FormatInt(exp, 10) + `,"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `","chatgpt_plan_type":"pro"}}`,
	))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return h + "." + p + "." + s
}

// findStateInPrintedURL extracts the state query param from the auth
// URL printed to stdout, so the test can fabricate a matching pasted
// redirect URL.
func findStateInPrintedURL(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, "https://auth.openai.com/oauth/authorize?")
	if idx < 0 {
		t.Fatalf("no authorize URL in stdout: %s", out)
	}
	rest := out[idx:]
	end := strings.IndexAny(rest, " \n\t")
	if end > 0 {
		rest = rest[:end]
	}
	u, err := url.Parse(rest)
	if err != nil {
		t.Fatalf("parse printed URL: %v", err)
	}
	return u.Query().Get("state")
}

// safeBuffer is a bytes.Buffer guarded by a mutex for concurrent access.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *safeBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *safeBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

// loginAsync starts Login in a goroutine, drains stdout into a
// mutex-protected buffer, waits for the prompt to appear, then writes
// the URL returned by pastedURL(state) to stdin and closes it.
// Returns channels for the credential and error.
func loginAsync(t *testing.T, pastedURL func(state string) string) (<-chan *store.Credential, <-chan error) {
	t.Helper()
	prIn, pwIn := io.Pipe()
	prOut, pwOut := io.Pipe()

	credCh := make(chan *store.Credential, 1)
	errCh := make(chan error, 1)

	go func() {
		c, e := codexoauth.Login(context.Background(), pwOut, prIn)
		_ = pwOut.Close()
		credCh <- c
		errCh <- e
	}()

	out := &safeBuffer{}
	drainDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, prOut)
		close(drainDone)
	}()

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		var state string
		for time.Now().Before(deadline) {
			s := out.String()
			if strings.Contains(s, "https://auth.openai.com/oauth/authorize?") && strings.Contains(s, "> ") {
				state = findStateInPrintedURL(t, s)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		pasted := pastedURL(state)
		_, _ = pwIn.Write([]byte(pasted))
		_ = pwIn.Close()
		<-drainDone
	}()

	return credCh, errCh
}

func tokenServer(t *testing.T, email, accountID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		tok := mkLoginJWT(t, email, accountID)
		_, _ = w.Write([]byte(`{"access_token":"` + tok + `","refresh_token":"new_rt","id_token":"` + tok + `","expires_in":600}`))
	}))
}

func setTokenURLStr(t *testing.T, u string) {
	t.Helper()
	prev := codexoauth.TokenURL
	codexoauth.TokenURL = u
	t.Cleanup(func() { codexoauth.TokenURL = prev })
}

// ---- Login integration tests ----

func TestLogin_HappyPath_PasteURL(t *testing.T) {
	srv := tokenServer(t, "u@x.com", "acct-1")
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	credCh, errCh := loginAsync(t, func(state string) string {
		if state == "" {
			t.Errorf("state was empty — printed URL not captured in time")
		}
		return "http://localhost:1455/auth/callback?code=THE_CODE&state=" + state + "\n"
	})

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cred := <-credCh
	if cred == nil || cred.Tokens == nil || cred.Tokens.AccountID != "acct-1" {
		t.Fatalf("account_id missing: %+v", cred)
	}
	if cred.Provider != "codex" {
		t.Fatalf("provider: %q", cred.Provider)
	}
	if cred.Name != "u@x.com" {
		t.Fatalf("name: %q", cred.Name)
	}
	if cred.AuthMode != "chatgpt" {
		t.Fatalf("auth_mode: %q", cred.AuthMode)
	}
	if cred.OpenAIAPIKey != nil {
		t.Fatal("OpenAIAPIKey: want nil")
	}
	if cred.LastRefresh == "" {
		t.Fatal("LastRefresh empty")
	}
}

func TestLogin_EmptyEmail_FallsBackToUUIDPrefix(t *testing.T) {
	srv := tokenServer(t, "", "acct-3")
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	credCh, errCh := loginAsync(t, func(state string) string {
		return "http://localhost:1455/auth/callback?code=c&state=" + state + "\n"
	})

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cred := <-credCh
	if cred == nil || cred.Name == "" {
		t.Fatal("name should fall back to uuid prefix, got empty")
	}
	if len(cred.Name) > 8 {
		t.Fatalf("uuid prefix should be ≤8 chars; got %q", cred.Name)
	}
}

func TestLogin_ExchangeError_Propagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	credCh, errCh := loginAsync(t, func(state string) string {
		return "http://localhost:1455/auth/callback?code=bad&state=" + state + "\n"
	})
	_ = <-credCh

	if err := <-errCh; err == nil {
		t.Fatal("expected error from ExchangeCode, got nil")
	}
}

func TestLogin_StateMismatch(t *testing.T) {
	prIn, pwIn := io.Pipe()
	prOut, pwOut := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, e := codexoauth.Login(context.Background(), pwOut, prIn)
		_ = pwOut.Close()
		done <- e
	}()
	// Drain stdout
	go io.Copy(io.Discard, prOut) //nolint:errcheck

	// Wait briefly for the prompt to be printed.
	time.Sleep(100 * time.Millisecond)
	pasted := "http://localhost:1455/auth/callback?code=abc&state=WRONG\n"
	_, _ = pwIn.Write([]byte(pasted))
	_ = pwIn.Close()

	if err := <-done; !errors.Is(err, codexoauth.ErrStateMismatch) {
		t.Fatalf("want ErrStateMismatch; got %v", err)
	}
}

func TestLogin_StdinEOF_Errors(t *testing.T) {
	stdout := &bytes.Buffer{}
	stdin := strings.NewReader("") // immediate EOF, no content
	_, err := codexoauth.Login(context.Background(), stdout, stdin)
	if err == nil {
		t.Fatal("expected error on EOF")
	}
}

// TestLogin_ParseCallbackError_Propagates covers the parseCallbackURL
// error path inside Login (lines 47-49): paste an access_denied URL
// so parseCallbackURL returns ErrAuthDenied before the state check.
func TestLogin_ParseCallbackError_Propagates(t *testing.T) {
	prIn, pwIn := io.Pipe()
	prOut, pwOut := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, e := codexoauth.Login(context.Background(), pwOut, prIn)
		_ = pwOut.Close()
		done <- e
	}()
	go io.Copy(io.Discard, prOut) //nolint:errcheck

	time.Sleep(100 * time.Millisecond)
	// Paste an access_denied URL — parseCallbackURL returns ErrAuthDenied
	// before the state equality check, covering the error-return branch.
	_, _ = pwIn.Write([]byte("http://localhost:1455/auth/callback?error=access_denied\n"))
	_ = pwIn.Close()

	if err := <-done; !errors.Is(err, codexoauth.ErrAuthDenied) {
		t.Fatalf("want ErrAuthDenied; got %v", err)
	}
}

// ---- parseCallbackURL unit tests (via exported seam) ----

func TestParseCallbackURL_HappyPath(t *testing.T) {
	code, state, err := codexoauth.ExportedParseCallbackURL("http://localhost:1455/auth/callback?code=abc&state=xyz")
	if err != nil {
		t.Fatal(err)
	}
	if code != "abc" || state != "xyz" {
		t.Fatalf("got code=%q state=%q", code, state)
	}
}

func TestParseCallbackURL_AccessDenied(t *testing.T) {
	_, _, err := codexoauth.ExportedParseCallbackURL("http://localhost:1455/auth/callback?error=access_denied&state=x")
	if !errors.Is(err, codexoauth.ErrAuthDenied) {
		t.Fatalf("want ErrAuthDenied; got %v", err)
	}
}

func TestParseCallbackURL_OtherError_Wraps(t *testing.T) {
	_, _, err := codexoauth.ExportedParseCallbackURL("http://localhost:1455/auth/callback?error=server_error&error_description=oops&state=x")
	if !errors.Is(err, codexoauth.ErrTokenEndpoint) {
		t.Fatalf("want ErrTokenEndpoint; got %v", err)
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Fatalf("missing description: %v", err)
	}
}

func TestParseCallbackURL_OtherError_NoDesc_FallsBackToCode(t *testing.T) {
	_, _, err := codexoauth.ExportedParseCallbackURL("http://localhost:1455/auth/callback?error=server_error")
	if err == nil || !strings.Contains(err.Error(), "server_error") {
		t.Fatalf("want server_error in msg; got %v", err)
	}
}

func TestParseCallbackURL_NoCode_Errors(t *testing.T) {
	_, _, err := codexoauth.ExportedParseCallbackURL("http://localhost:1455/auth/callback?state=x")
	if err == nil || !strings.Contains(err.Error(), "no code") {
		t.Fatalf("want no-code error; got %v", err)
	}
}

func TestParseCallbackURL_Empty_Errors(t *testing.T) {
	_, _, err := codexoauth.ExportedParseCallbackURL("   ")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty error; got %v", err)
	}
}

func TestParseCallbackURL_BadURL_Errors(t *testing.T) {
	_, _, err := codexoauth.ExportedParseCallbackURL("not://a valid url with spaces")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLogin_PopulatesTierFromUsage(t *testing.T) {
	srv := tokenServer(t, "user@example.com", "acct-tier")
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	// Stub the usage seam to return a canned tier.
	restore := codexoauth.SeamLoginUsage(func(at, acct string) *oauth_pkg.UsageInfo {
		return &oauth_pkg.UsageInfo{Tier: "Pro"}
	})
	defer restore()

	credCh, errCh := loginAsync(t, func(state string) string {
		return "http://localhost:1455/auth/callback?code=THE_CODE&state=" + state + "\n"
	})

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cred := <-credCh
	if cred == nil {
		t.Fatal("expected credential, got nil")
	}
	if cred.Subscription.Tier != "Pro" {
		t.Fatalf("Subscription.Tier = %q, want Pro", cred.Subscription.Tier)
	}
}

func TestLogin_UsageFailure_TierEmpty(t *testing.T) {
	srv := tokenServer(t, "user@example.com", "acct-err")
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	// Stub usage to return error (tier should remain empty).
	restore := codexoauth.SeamLoginUsage(func(at, acct string) *oauth_pkg.UsageInfo {
		return &oauth_pkg.UsageInfo{Error: "HTTP 503"}
	})
	defer restore()

	credCh, errCh := loginAsync(t, func(state string) string {
		return "http://localhost:1455/auth/callback?code=c&state=" + state + "\n"
	})

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cred := <-credCh
	if cred == nil {
		t.Fatal("expected credential, got nil")
	}
	// Empty Tier because usage Tier field is empty (error case).
	if cred.Subscription.Tier != "" {
		t.Fatalf("Subscription.Tier = %q, want empty when usage fails", cred.Subscription.Tier)
	}
}
