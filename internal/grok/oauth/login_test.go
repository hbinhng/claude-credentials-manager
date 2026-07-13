package grokoauth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	grokoauth "github.com/hbinhng/claude-credentials-manager/internal/grok/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestParseCallbackURL_Errors(t *testing.T) {
	if _, _, err := grokoauth.ExportedParseCallbackURL(""); err == nil {
		t.Error("empty URL should error")
	}
	if _, _, err := grokoauth.ExportedParseCallbackURL("http://x/?error=access_denied"); err == nil {
		t.Error("access_denied should error")
	}
	code, state, err := grokoauth.ExportedParseCallbackURL("http://x/?code=C&state=S")
	if err != nil || code != "C" || state != "S" {
		t.Fatalf("got %q/%q err=%v", code, state, err)
	}
}

func TestParseCallbackURL_BadURL_Errors(t *testing.T) {
	_, _, err := grokoauth.ExportedParseCallbackURL("not://a valid url with spaces")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseCallbackURL_OtherError_Wraps(t *testing.T) {
	_, _, err := grokoauth.ExportedParseCallbackURL("http://x/?error=server_error&error_description=oops&state=s")
	if !errors.Is(err, grokoauth.ErrTokenEndpoint) {
		t.Fatalf("want ErrTokenEndpoint; got %v", err)
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Fatalf("missing description: %v", err)
	}
}

func TestParseCallbackURL_OtherError_NoDesc_FallsBackToCode(t *testing.T) {
	_, _, err := grokoauth.ExportedParseCallbackURL("http://x/?error=server_error")
	if err == nil || !strings.Contains(err.Error(), "server_error") {
		t.Fatalf("want server_error in msg; got %v", err)
	}
}

func TestParseCallbackURL_NoCode_Errors(t *testing.T) {
	_, _, err := grokoauth.ExportedParseCallbackURL("http://x/?state=s")
	if err == nil || !strings.Contains(err.Error(), "no code") {
		t.Fatalf("want no-code error; got %v", err)
	}
}

func TestLogin_StateMismatch(t *testing.T) {
	in := strings.NewReader("http://127.0.0.1:56121/callback?code=C&state=WRONG\n")
	_, err := grokoauth.Login(context.Background(), new(bytes.Buffer), in)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("want state mismatch, got %v", err)
	}
}

func TestLogin_EmptyStdin(t *testing.T) {
	_, err := grokoauth.Login(context.Background(), new(bytes.Buffer), strings.NewReader(""))
	if err == nil {
		t.Fatal("want error on empty stdin")
	}
}

// ---- Login happy-path (state-capture async harness, mirrors codexoauth) ----

// safeBuffer is a bytes.Buffer guarded by a mutex for concurrent access
// (Login writes the prompt to stdout while the test goroutine reads it).
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

// findStateInPrintedURL extracts the state query param from the authorize
// URL printed to stdout, so the test can fabricate a matching pasted
// redirect URL.
func findStateInPrintedURL(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, "https://auth.x.ai/oauth2/authorize?")
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

// loginAsync starts Login in a goroutine, drains stdout into a
// mutex-protected buffer, waits for the authorize URL + prompt to appear,
// then writes the URL returned by pastedURL(state) to stdin and closes it.
func loginAsync(t *testing.T, pastedURL func(state string) string) (<-chan *store.Credential, <-chan error) {
	t.Helper()
	prIn, pwIn := io.Pipe()
	prOut, pwOut := io.Pipe()

	credCh := make(chan *store.Credential, 1)
	errCh := make(chan error, 1)

	go func() {
		c, e := grokoauth.Login(context.Background(), pwOut, prIn)
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
			if strings.Contains(s, "https://auth.x.ai/oauth2/authorize?") && strings.Contains(s, "> ") {
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

// mkIDToken builds an unsigned JWT-shaped string (header.payload.sig) whose
// payload carries the given email claim, matching what claimEmail parses.
func mkIDToken(t *testing.T, email string) string {
	t.Helper()
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return h + "." + p + "." + s
}

func setTokenURLStr(t *testing.T, u string) {
	t.Helper()
	prev := grokoauth.TokenURL
	grokoauth.TokenURL = u
	t.Cleanup(func() { grokoauth.TokenURL = prev })
}

func TestLogin_HappyPath(t *testing.T) {
	idToken := mkIDToken(t, "me@x.ai")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"id_token":"` + idToken + `"}`))
	}))
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	before := time.Now()
	credCh, errCh := loginAsync(t, func(state string) string {
		if state == "" {
			t.Errorf("state was empty — printed URL not captured in time")
		}
		return "http://127.0.0.1:56121/callback?code=THE_CODE&state=" + state + "\n"
	})

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cred := <-credCh
	if cred == nil {
		t.Fatal("expected credential, got nil")
	}
	if cred.Provider != "grok" {
		t.Fatalf("Provider = %q, want grok", cred.Provider)
	}
	if got := cred.AccessToken(); got != "a" {
		t.Fatalf("AccessToken() = %q, want a", got)
	}
	if got := cred.RefreshToken(); got != "r" {
		t.Fatalf("RefreshToken() = %q, want r", got)
	}
	if cred.Name != "me@x.ai" {
		t.Fatalf("Name = %q, want me@x.ai", cred.Name)
	}
	wantMin := before.Add(3599 * time.Second).UnixMilli()
	wantMax := time.Now().Add(3601 * time.Second).UnixMilli()
	if got := cred.ExpiresAtMillis(); got < wantMin || got > wantMax {
		t.Fatalf("ExpiresAtMillis() = %d, want between %d and %d", got, wantMin, wantMax)
	}
}

// TestLogin_ParseCallbackError_Propagates covers the parseCallbackURL
// error-return branch inside Login (before the state check): paste an
// access_denied URL so parseCallbackURL returns ErrAuthDenied.
func TestLogin_ParseCallbackError_Propagates(t *testing.T) {
	credCh, errCh := loginAsync(t, func(state string) string {
		return "http://127.0.0.1:56121/callback?error=access_denied&state=" + state + "\n"
	})
	_ = <-credCh

	if err := <-errCh; !errors.Is(err, grokoauth.ErrAuthDenied) {
		t.Fatalf("want ErrAuthDenied; got %v", err)
	}
}

// TestLogin_ExchangeError_Propagates covers the ExchangeCode error-return
// branch inside Login: the token endpoint rejects the code.
func TestLogin_ExchangeError_Propagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	credCh, errCh := loginAsync(t, func(state string) string {
		return "http://127.0.0.1:56121/callback?code=bad&state=" + state + "\n"
	})
	_ = <-credCh

	if err := <-errCh; err == nil {
		t.Fatal("expected error from ExchangeCode, got nil")
	}
}

// TestLogin_EmptyEmail_FallsBackToUUIDPrefix covers the claimEmail=="" branch
// where Login falls back to an 8-char uuid prefix for the credential name.
func TestLogin_EmptyEmail_FallsBackToUUIDPrefix(t *testing.T) {
	idToken := mkIDToken(t, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"id_token":"` + idToken + `"}`))
	}))
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	credCh, errCh := loginAsync(t, func(state string) string {
		return "http://127.0.0.1:56121/callback?code=c&state=" + state + "\n"
	})

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cred := <-credCh
	if cred == nil || cred.Name == "" {
		t.Fatal("name should fall back to uuid prefix, got empty")
	}
	if len(cred.Name) > 8 {
		t.Fatalf("uuid prefix should be <=8 chars; got %q", cred.Name)
	}
}

// TestLogin_BareCodePaste covers pasting just the authorization code
// (no redirect URL). Detection is by http(s) prefix: input without one is
// treated as the bare code, and no state validation applies (there is no
// state to compare). The code must reach ExchangeCode verbatim.
func TestLogin_BareCodePaste(t *testing.T) {
	idToken := mkIDToken(t, "bare@x.ai")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.Form.Get("code"); got != "BARECODE123" {
			t.Errorf("exchange code = %q, want BARECODE123", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"id_token":"` + idToken + `"}`))
	}))
	defer srv.Close()
	setTokenURLStr(t, srv.URL)

	// Surrounding whitespace should be trimmed; no state needed.
	cred, err := grokoauth.Login(context.Background(), new(bytes.Buffer), strings.NewReader("  BARECODE123  \n"))
	if err != nil {
		t.Fatalf("Login (bare code): %v", err)
	}
	if cred == nil {
		t.Fatal("expected credential, got nil")
	}
	if got := cred.AccessToken(); got != "a" {
		t.Fatalf("AccessToken() = %q, want a", got)
	}
	if cred.Name != "bare@x.ai" {
		t.Fatalf("Name = %q, want bare@x.ai", cred.Name)
	}
}
