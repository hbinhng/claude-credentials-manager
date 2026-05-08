package middleware

import (
	"net/http"
)

// BearerSource is the indirection used by UpstreamAuthReplace to fetch
// the credential's current bearer. The method name is Fresh() to match
// the existing tokenSource interface in internal/share/proxy.go that
// *credState already satisfies. Both interfaces are satisfied by the
// same concrete type via Go structural typing.
type BearerSource interface {
	Fresh() (string, error)
}

// UpstreamAuthReplace strips the inbound Authorization header and
// injects Bearer <bearerSrc.Fresh()> for the upstream request.
type UpstreamAuthReplace struct {
	src BearerSource
}

// NewUpstreamAuthReplace constructs the step.
func NewUpstreamAuthReplace(src BearerSource) *UpstreamAuthReplace {
	return &UpstreamAuthReplace{src: src}
}

// Apply wraps next.
func (u *UpstreamAuthReplace) Apply(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := u.src.Fresh()
		if err != nil {
			http.Error(w, "credential unavailable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		r.Header.Set("Authorization", "Bearer "+token)
		next.ServeHTTP(w, r)
	})
}
