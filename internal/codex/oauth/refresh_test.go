package codexoauth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

func setTokenURL(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	prev := codexoauth.TokenURL
	codexoauth.TokenURL = srv.URL
	t.Cleanup(func() { codexoauth.TokenURL = prev })
}

func TestRefresh_Success_RotatesTokens(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type")
		}
		if r.Form.Get("refresh_token") != "old_rt" {
			t.Fatalf("refresh_token")
		}
		if r.Form.Get("client_id") != codexoauth.ClientID {
			t.Fatalf("client_id")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new_at","refresh_token":"new_rt","id_token":"new_it","expires_in":600}`))
	})
	out, err := codexoauth.Refresh("old_rt")
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessToken != "new_at" || out.RefreshToken != "new_rt" || out.IDToken != "new_it" {
		t.Fatalf("response not parsed: %+v", out)
	}
}

func TestRefresh_4xx_InvalidGrant_ReturnsErrRefreshRotated(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Refresh token expired"}`))
	})
	_, err := codexoauth.Refresh("rt")
	if !errors.Is(err, codexoauth.ErrRefreshRotated) {
		t.Fatalf("want ErrRefreshRotated; got %v", err)
	}
}

func TestRefresh_4xx_RefreshTokenReused_ReturnsErrRefreshRotated(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"refresh_token_reused"}`))
	})
	_, err := codexoauth.Refresh("rt")
	if !errors.Is(err, codexoauth.ErrRefreshRotated) {
		t.Fatalf("want ErrRefreshRotated; got %v", err)
	}
}

func TestRefresh_4xx_TokenExpired_ReturnsErrRefreshRotated(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"token_expired"}`))
	})
	_, err := codexoauth.Refresh("rt")
	if !errors.Is(err, codexoauth.ErrRefreshRotated) {
		t.Fatalf("want ErrRefreshRotated; got %v", err)
	}
}

func TestRefresh_4xx_OtherError_ReturnsErrTokenEndpoint(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})
	_, err := codexoauth.Refresh("rt")
	if !errors.Is(err, codexoauth.ErrTokenEndpoint) {
		t.Fatalf("want ErrTokenEndpoint; got %v", err)
	}
}

func TestRefresh_5xx_ReturnsErrTokenEndpoint(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`upstream broke`))
	})
	_, err := codexoauth.Refresh("rt")
	if !errors.Is(err, codexoauth.ErrTokenEndpoint) {
		t.Fatalf("want ErrTokenEndpoint; got %v", err)
	}
}

func TestRefresh_RedactsTokensFromErrorMessages(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"oops","seen":"rt_abc.def"}`))
	})
	_, err := codexoauth.Refresh("rt")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "rt_abc.def") {
		t.Fatalf("rotating refresh token leaked into error: %v", err)
	}
}

func TestRefresh_NetworkError_Wrapped(t *testing.T) {
	prev := codexoauth.TokenURL
	codexoauth.TokenURL = "http://127.0.0.1:1" // refused
	defer func() { codexoauth.TokenURL = prev }()
	_, err := codexoauth.Refresh("rt")
	if err == nil {
		t.Fatal("expected network error")
	}
	if !strings.Contains(err.Error(), "post token endpoint") {
		t.Fatalf("unwrapped network error: %v", err)
	}
}

func TestRefresh_BadJSON_Errors(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	})
	_, err := codexoauth.Refresh("rt")
	if err == nil || !strings.Contains(err.Error(), "parse token response") {
		t.Fatalf("want parse error; got %v", err)
	}
}

func TestRefresh_InvalidTokenURL_ReturnsRequestBuildError(t *testing.T) {
	prev := codexoauth.TokenURL
	// A URL with a space in the scheme causes http.NewRequest to fail.
	codexoauth.TokenURL = "://invalid url"
	defer func() { codexoauth.TokenURL = prev }()
	_, err := codexoauth.Refresh("rt")
	if err == nil {
		t.Fatal("expected error from invalid URL")
	}
}

func TestExchangeCode_Success(t *testing.T) {
	setTokenURL(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Fatalf("grant_type")
		}
		if r.Form.Get("code") != "THE_CODE" {
			t.Fatalf("code")
		}
		if r.Form.Get("code_verifier") != "verifier" {
			t.Fatalf("code_verifier")
		}
		if r.Form.Get("redirect_uri") != codexoauth.DefaultRedirectURI {
			t.Fatalf("redirect_uri")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","id_token":"i","expires_in":600}`))
	})
	out, err := codexoauth.ExchangeCode("THE_CODE", "verifier", codexoauth.DefaultRedirectURI)
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessToken != "a" || out.RefreshToken != "r" {
		t.Fatalf("response: %+v", out)
	}
}
