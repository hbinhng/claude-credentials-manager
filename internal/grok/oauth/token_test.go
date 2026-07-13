package grokoauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600}`))
	}))
	defer srv.Close()
	old := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = old }()

	tr, err := ExchangeCode("code", "verifier", "http://127.0.0.1:56121/callback")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tr.AccessToken != "a" || tr.RefreshToken != "r" || tr.ExpiresIn != 3600 {
		t.Fatalf("bad token response: %+v", tr)
	}
}

func TestRefresh_InvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	old := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = old }()

	_, err := Refresh("dead")
	if !errors.Is(err, ErrRefreshRotated) {
		t.Fatalf("want ErrRefreshRotated, got %v", err)
	}
}

func TestRefresh_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()
	old := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = old }()

	_, err := Refresh("x")
	if !errors.Is(err, ErrTokenEndpoint) {
		t.Fatalf("want ErrTokenEndpoint, got %v", err)
	}
}

func TestExchangeCode_JSONParseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{invalid json}`))
	}))
	defer srv.Close()
	old := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = old }()

	_, err := ExchangeCode("code", "verifier", "http://127.0.0.1:56121/callback")
	if err == nil {
		t.Fatalf("want error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parse token response") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestRefresh_NetworkError(t *testing.T) {
	old := TokenURL
	TokenURL = "http://127.0.0.1:1/nonexistent"
	defer func() { TokenURL = old }()

	_, err := Refresh("token")
	if err == nil {
		t.Fatalf("want network error, got nil")
	}
	if !strings.Contains(err.Error(), "post token endpoint") {
		t.Fatalf("want post token endpoint error, got %v", err)
	}
}

func TestExchangeCode_InvalidTokenURL(t *testing.T) {
	old := TokenURL
	TokenURL = "ht!tp://invalid url"
	defer func() { TokenURL = old }()

	_, err := ExchangeCode("code", "verifier", "http://127.0.0.1:56121/callback")
	if err == nil {
		t.Fatalf("want error for invalid TokenURL, got nil")
	}
}
