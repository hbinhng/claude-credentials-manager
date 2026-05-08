package codexoauth_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

func setEphemeralListenAddr(t *testing.T) {
	t.Helper()
	prev := codexoauth.ListenAddr
	codexoauth.ListenAddr = "127.0.0.1:0"
	t.Cleanup(func() { codexoauth.ListenAddr = prev })
}

func TestCallback_Success(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + addr + "/auth/callback?code=THE_CODE&state=st")
	}()

	code, err := srv.Wait(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != "THE_CODE" {
		t.Fatalf("code: %q", code)
	}
}

func TestCallback_StateMismatch(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("expected")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + addr + "/auth/callback?code=c&state=wrong")
	}()
	_, err = srv.Wait(2 * time.Second)
	if !errors.Is(err, codexoauth.ErrStateMismatch) {
		t.Fatalf("want ErrStateMismatch; got %v", err)
	}
}

func TestCallback_AuthDenied(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + addr + "/auth/callback?error=access_denied&state=st")
	}()
	_, err = srv.Wait(2 * time.Second)
	if !errors.Is(err, codexoauth.ErrAuthDenied) {
		t.Fatalf("want ErrAuthDenied; got %v", err)
	}
}

func TestCallback_OtherError_ReturnsTokenEndpointWithDesc(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + addr + "/auth/callback?error=server_error&error_description=oops&state=st")
	}()
	_, err = srv.Wait(2 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("want error containing 'oops'; got %v", err)
	}
}

func TestCallback_OtherError_NoDesc_FallsBackToErrorCode(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + addr + "/auth/callback?error=server_error&state=st")
	}()
	_, err = srv.Wait(2 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "server_error") {
		t.Fatalf("want fallback to error code; got %v", err)
	}
}

func TestCallback_MissingCode_Errors(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + addr + "/auth/callback?state=st") // no code
	}()
	_, err = srv.Wait(2 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "missing code") {
		t.Fatalf("want missing-code error; got %v", err)
	}
}

func TestCallback_Timeout(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, _, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	_, err = srv.Wait(50 * time.Millisecond)
	if !errors.Is(err, codexoauth.ErrCallbackTimeout) {
		t.Fatalf("want ErrCallbackTimeout; got %v", err)
	}
}

func TestStartCallbackServer_PortInUse_ReturnsErrPortInUse(t *testing.T) {
	hold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()

	prev := codexoauth.ListenAddr
	codexoauth.ListenAddr = hold.Addr().String()
	defer func() { codexoauth.ListenAddr = prev }()

	if _, _, err := codexoauth.StartCallbackServer("st"); !errors.Is(err, codexoauth.ErrPortInUse) {
		t.Fatalf("want ErrPortInUse; got %v", err)
	}
}

func TestCallback_RendersClosePromptHTML(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())

	resp, err := http.Get("http://" + addr + "/auth/callback?code=c&state=st")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "You can close this tab") {
		t.Fatalf("HTML response missing close prompt: %s", body)
	}
	// Body must not echo the auth code.
	if strings.Contains(string(body), `code=c`) || strings.Contains(string(body), `>c<`) {
		t.Fatalf("HTML response leaked code: %s", body)
	}
	// Drain the channel.
	srv.Wait(time.Second)
}

func TestCallback_DoubleHit_SecondReturnsClosePromptOnly(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, addr, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())

	_, _ = http.Get("http://" + addr + "/auth/callback?code=c&state=st")
	resp, err := http.Get("http://" + addr + "/auth/callback?code=c&state=st")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "You can close this tab") {
		t.Fatalf("second hit should still render close prompt: %s", body)
	}
	srv.Wait(time.Second)
}

func TestServer_ShutdownIsIdempotent(t *testing.T) {
	setEphemeralListenAddr(t)
	srv, _, err := codexoauth.StartCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

func TestStartCallbackServer_NonPortInUseBind_WrapsError(t *testing.T) {
	prev := codexoauth.ListenAddr
	// An invalid address (bad host) causes a non-EADDRINUSE bind error.
	codexoauth.ListenAddr = "256.256.256.256:1455"
	defer func() { codexoauth.ListenAddr = prev }()

	_, _, err := codexoauth.StartCallbackServer("st")
	if err == nil {
		t.Fatal("expected bind error")
	}
	if errors.Is(err, codexoauth.ErrPortInUse) {
		t.Fatalf("got ErrPortInUse for bad address, want wrapped generic error")
	}
	if !strings.Contains(err.Error(), "bind callback listener") {
		t.Fatalf("expected 'bind callback listener' in error, got: %v", err)
	}
}
