// Package pipeline provides a small middleware chain framework for the
// share proxy. Steps wrap each other in declared order: the first step
// is the outermost wrapper, the last is closest to the terminal handler.
//
// Each Step can mutate the request, mutate the response writer, inject
// values into the request context, or short-circuit by not calling next.
//
// All Steps return a handler that recovers from panics and returns 500
// to the client (without leaking panic details).
package pipeline

import (
	"log"
	"net/http"
	"runtime/debug"
)

// Step wraps a downstream handler. Implementations should call next.ServeHTTP
// to continue the chain, or skip it to short-circuit.
type Step interface {
	Apply(next http.Handler) http.Handler
}

// Pipeline is an ordered chain of Steps.
type Pipeline struct {
	steps []Step
}

// New creates a Pipeline. Steps are applied in declaration order: the first
// is outermost.
func New(steps ...Step) *Pipeline {
	return &Pipeline{steps: steps}
}

// Handler returns an http.Handler that runs the pipeline and finishes with
// terminal. Empty pipeline returns terminal wrapped in panic recovery.
func (p *Pipeline) Handler(terminal http.Handler) http.Handler {
	h := terminal
	for i := len(p.steps) - 1; i >= 0; i-- {
		h = p.steps[i].Apply(h)
	}
	return recoverPanics(h)
}

func recoverPanics(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("pipeline: panic recovered: %v\n%s", v, debug.Stack())
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		h.ServeHTTP(w, r)
	})
}
