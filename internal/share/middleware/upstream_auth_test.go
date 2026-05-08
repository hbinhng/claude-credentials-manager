package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
)

type fakeBearerSrc struct{ token string }

func (f fakeBearerSrc) Fresh() (string, error) { return f.token, nil }

func TestUpstreamAuthReplace_OverridesAuthHeader(t *testing.T) {
	mw := middleware.NewUpstreamAuthReplace(fakeBearerSrc{token: "cred-bearer"})
	var seen string
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	})

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer downstream-secret")
	mw.Apply(terminal).ServeHTTP(httptest.NewRecorder(), req)

	if seen != "Bearer cred-bearer" {
		t.Errorf("Authorization = %q, want Bearer cred-bearer", seen)
	}
}

func TestUpstreamAuthReplace_BearerSrcError500(t *testing.T) {
	mw := middleware.NewUpstreamAuthReplace(errSrc{})
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("terminal called despite cred error")
	})

	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	mw.Apply(terminal).ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

type errSrc struct{}

func (errSrc) Fresh() (string, error) {
	return "", errBoom{}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
