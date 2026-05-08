package middleware

import (
	"net/http"
	"strings"
)

// DownstreamAuth validates the inbound Authorization header against a
// shared secret. Empty secret = pass-through (launch mode, no auth).
type DownstreamAuth struct {
	expected string
}

// NewDownstreamAuth constructs a DownstreamAuth step. If expected is "",
// the step is a no-op (launch mode).
func NewDownstreamAuth(expected string) *DownstreamAuth {
	return &DownstreamAuth{expected: expected}
}

// Apply wraps next.
func (a *DownstreamAuth) Apply(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.expected == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(got, prefix) || strings.TrimSpace(got[len(prefix):]) != a.expected {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ccm"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
