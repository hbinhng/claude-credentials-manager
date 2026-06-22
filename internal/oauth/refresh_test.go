package oauth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func withTokenURL(t *testing.T, url string) {
	t.Helper()
	orig := TokenURL
	TokenURL = url
	t.Cleanup(func() { TokenURL = orig })
}

func TestRefresh_InvalidGrant_ReturnsErrInvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	}))
	defer srv.Close()
	withTokenURL(t, srv.URL)

	_, err := Refresh("dead-token")
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("want errors.Is ErrInvalidGrant; got %v", err)
	}
}

func TestRefresh_OtherErrorCode_NotInvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
	}))
	defer srv.Close()
	withTokenURL(t, srv.URL)

	_, err := Refresh("x")
	if err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("want a non-ErrInvalidGrant error; got %v", err)
	}
}

func TestRefresh_5xx_NotInvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream boom`))
	}))
	defer srv.Close()
	withTokenURL(t, srv.URL)

	_, err := Refresh("x")
	if err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("want a non-ErrInvalidGrant transient error; got %v", err)
	}
}

func TestErrInvalidGrant_SurvivesWrapping(t *testing.T) {
	// Mirrors credState.Fresh's `refresh: %w` wrapping over oauth.Refresh's error.
	inner := fmt.Errorf("refresh failed (HTTP 400): {...}: %w", ErrInvalidGrant)
	wrapped := fmt.Errorf("refresh: %w", inner)
	if !errors.Is(wrapped, ErrInvalidGrant) {
		t.Fatal("errors.Is must hold through the %w chain")
	}
}
