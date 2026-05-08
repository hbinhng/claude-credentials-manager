package codexoauth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

// listenFreePort binds an ephemeral TCP port and returns the listener.
// The caller must close the listener when done.
func listenFreePort() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

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

func setLoginEphemeral(t *testing.T) {
	t.Helper()
	prevAddr := codexoauth.ListenAddr
	codexoauth.ListenAddr = "127.0.0.1:0"
	t.Cleanup(func() { codexoauth.ListenAddr = prevAddr })
}

func setOpenBrowser(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := codexoauth.OpenBrowser
	codexoauth.OpenBrowser = fn
	t.Cleanup(func() { codexoauth.OpenBrowser = prev })
}

func setSeamLoginContext(t *testing.T, fn func(codexoauth.LoginContext)) {
	t.Helper()
	prev := codexoauth.SeamLoginContext
	codexoauth.SeamLoginContext = fn
	t.Cleanup(func() { codexoauth.SeamLoginContext = prev })
}

func setLoginTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := codexoauth.LoginTimeout
	codexoauth.LoginTimeout = d
	t.Cleanup(func() { codexoauth.LoginTimeout = prev })
}

func TestLogin_HappyPath_DriversCallbackAndExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + mkLoginJWT(t, "u@x.com", "acct-1") + `","refresh_token":"new_rt","id_token":"` + mkLoginJWT(t, "u@x.com", "acct-1") + `","expires_in":600}`))
	}))
	defer srv.Close()
	prevTok := codexoauth.TokenURL
	codexoauth.TokenURL = srv.URL
	defer func() { codexoauth.TokenURL = prevTok }()

	setLoginEphemeral(t)
	setOpenBrowser(t, func(string) error { return nil })

	captured := make(chan codexoauth.LoginContext, 1)
	setSeamLoginContext(t, func(c codexoauth.LoginContext) { captured <- c })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c := <-captured
		_, _ = http.Get("http://" + c.BindAddr + "/auth/callback?code=THE_CODE&state=" + c.State)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cred, err := codexoauth.Login(ctx)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if cred.Tokens == nil || cred.Tokens.AccountID != "acct-1" {
		t.Fatalf("account_id missing: %+v", cred)
	}
	if cred.Name != "u@x.com" {
		t.Fatalf("name: %q", cred.Name)
	}
	if cred.Provider != "codex" {
		t.Fatalf("provider: %q", cred.Provider)
	}
	if cred.AuthMode != "chatgpt" {
		t.Fatalf("auth_mode")
	}
	if cred.OpenAIAPIKey != nil {
		t.Fatalf("OPENAI_API_KEY: want nil")
	}
	if cred.LastRefresh == "" {
		t.Fatalf("last_refresh empty")
	}
}

func TestLogin_TimeoutPropagates(t *testing.T) {
	setLoginEphemeral(t)
	setOpenBrowser(t, func(string) error { return nil })
	setLoginTimeout(t, 50*time.Millisecond)
	_, err := codexoauth.Login(context.Background())
	if !errors.Is(err, codexoauth.ErrCallbackTimeout) {
		t.Fatalf("want ErrCallbackTimeout; got %v", err)
	}
}

func TestLogin_BrowserFailureContinues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + mkLoginJWT(t, "u@x.com", "acct-2") + `","refresh_token":"r","id_token":"` + mkLoginJWT(t, "u@x.com", "acct-2") + `","expires_in":600}`))
	}))
	defer srv.Close()
	prev := codexoauth.TokenURL
	codexoauth.TokenURL = srv.URL
	defer func() { codexoauth.TokenURL = prev }()

	setLoginEphemeral(t)
	setOpenBrowser(t, func(string) error { return errors.New("no display") })

	captured := make(chan codexoauth.LoginContext, 1)
	setSeamLoginContext(t, func(c codexoauth.LoginContext) { captured <- c })

	go func() {
		c := <-captured
		_, _ = http.Get("http://" + c.BindAddr + "/auth/callback?code=c&state=" + c.State)
	}()
	cred, err := codexoauth.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cred.Tokens.AccountID != "acct-2" {
		t.Fatal("account_id wrong")
	}
}

func TestLogin_ExchangeCodeError_PropagatesErr(t *testing.T) {
	// Token endpoint returns a 400 so ExchangeCode returns an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	prev := codexoauth.TokenURL
	codexoauth.TokenURL = srv.URL
	defer func() { codexoauth.TokenURL = prev }()

	setLoginEphemeral(t)
	setOpenBrowser(t, func(string) error { return nil })

	captured := make(chan codexoauth.LoginContext, 1)
	setSeamLoginContext(t, func(c codexoauth.LoginContext) { captured <- c })

	go func() {
		c := <-captured
		_, _ = http.Get("http://" + c.BindAddr + "/auth/callback?code=bad&state=" + c.State)
	}()
	_, err := codexoauth.Login(context.Background())
	if err == nil {
		t.Fatal("expected error from ExchangeCode, got nil")
	}
}

func TestLogin_PortInUse_PropagatesErrPortInUse(t *testing.T) {
	// Bind a listener on 127.0.0.1:0, get the port, then point ListenAddr
	// at that same port to trigger the "address already in use" path.
	held, err := listenFreePort()
	if err != nil {
		t.Skip("could not bind ephemeral port:", err)
	}
	defer held.Close()
	addr := held.Addr().String()

	prev := codexoauth.ListenAddr
	codexoauth.ListenAddr = addr
	defer func() { codexoauth.ListenAddr = prev }()

	_, loginErr := codexoauth.Login(context.Background())
	if !errors.Is(loginErr, codexoauth.ErrPortInUse) {
		t.Fatalf("want ErrPortInUse; got %v", loginErr)
	}
}

func TestLogin_EmptyEmail_FallsBackToUUIDPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// id_token has empty email
		emptyEmail := mkLoginJWT(t, "", "acct-3")
		_, _ = w.Write([]byte(`{"access_token":"` + emptyEmail + `","refresh_token":"r","id_token":"` + emptyEmail + `","expires_in":600}`))
	}))
	defer srv.Close()
	prev := codexoauth.TokenURL
	codexoauth.TokenURL = srv.URL
	defer func() { codexoauth.TokenURL = prev }()
	setLoginEphemeral(t)
	setOpenBrowser(t, func(string) error { return nil })

	captured := make(chan codexoauth.LoginContext, 1)
	setSeamLoginContext(t, func(c codexoauth.LoginContext) { captured <- c })
	go func() {
		c := <-captured
		_, _ = http.Get("http://" + c.BindAddr + "/auth/callback?code=c&state=" + c.State)
	}()
	cred, err := codexoauth.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cred.Name == "" {
		t.Fatal("name should fall back to uuid prefix, got empty")
	}
	if len(cred.Name) > 8 {
		t.Fatalf("uuid prefix should be ≤8 chars; got %q", cred.Name)
	}
}
