package middleware

import (
	"net/http"
)

// CredSemaphore caps concurrent in-flight requests for a single
// credential. Capacity 0 = no gating (no-op).
type CredSemaphore struct {
	sem chan struct{}
}

// NewCredSemaphore constructs a semaphore with the given capacity.
// Capacity <= 0 returns a no-op step.
func NewCredSemaphore(capacity int) *CredSemaphore {
	if capacity <= 0 {
		return &CredSemaphore{sem: nil}
	}
	return &CredSemaphore{sem: make(chan struct{}, capacity)}
}

// Apply wraps next. Acquires a slot before the call; releases on return.
// Respects ctx cancellation.
func (s *CredSemaphore) Apply(next http.Handler) http.Handler {
	if s.sem == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
			next.ServeHTTP(w, r)
		case <-r.Context().Done():
			http.Error(w, "request cancelled while waiting for cred slot", 499)
		}
	})
}
