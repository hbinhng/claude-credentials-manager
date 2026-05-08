package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
)

func TestDownstreamAuth_Allow(t *testing.T) {
	mw := middleware.NewDownstreamAuth("expected-secret")
	called := false
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	h := mw.Apply(terminal)

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer expected-secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !called {
		t.Error("terminal not called when bearer matches")
	}
	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rr.Code)
	}
}

func TestDownstreamAuth_Deny(t *testing.T) {
	mw := middleware.NewDownstreamAuth("expected-secret")
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("terminal called despite auth failure")
	})
	h := mw.Apply(terminal)

	cases := []struct {
		name   string
		header string
		status int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"wrong secret", "Bearer wrong", http.StatusUnauthorized},
		{"empty bearer", "Bearer ", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != c.status {
				t.Errorf("status = %d, want %d", rr.Code, c.status)
			}
		})
	}
}

func TestDownstreamAuth_Empty_AllowsAll(t *testing.T) {
	// Empty shared secret = no downstream auth required (e.g. launch mode).
	mw := middleware.NewDownstreamAuth("")
	called := false
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := mw.Apply(terminal)

	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !called {
		t.Error("terminal not called with empty secret (launch-mode pass-through)")
	}
}
