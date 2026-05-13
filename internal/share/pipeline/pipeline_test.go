package pipeline_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/pipeline"
)

type recordStep struct {
	name string
	log  *[]string
}

func (s recordStep) Apply(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*s.log = append(*s.log, "before:"+s.name)
		next.ServeHTTP(w, r)
		*s.log = append(*s.log, "after:"+s.name)
	})
}

func TestPipeline_StepsRunInOrder(t *testing.T) {
	var log []string
	p := pipeline.New(
		recordStep{name: "a", log: &log},
		recordStep{name: "b", log: &log},
		recordStep{name: "c", log: &log},
	)
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log = append(log, "terminal")
		w.WriteHeader(http.StatusTeapot)
	})

	srv := httptest.NewServer(p.Handler(terminal))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()

	want := []string{"before:a", "before:b", "before:c", "terminal", "after:c", "after:b", "after:a"}
	if len(log) != len(want) {
		t.Fatalf("log len = %d, want %d; log = %v", len(log), len(want), log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Errorf("log[%d] = %q, want %q", i, log[i], want[i])
		}
	}
}

func TestPipeline_ContextPropagates(t *testing.T) {
	type ctxKey string
	injectStep := stepFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), ctxKey("k"), "v")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})

	var got string
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Value(ctxKey("k")).(string)
	})

	p := pipeline.New(injectStep)
	srv := httptest.NewServer(p.Handler(terminal))
	defer srv.Close()
	if _, err := http.Get(srv.URL); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v" {
		t.Errorf("got = %q, want v", got)
	}
}

func TestPipeline_PanicRecovered(t *testing.T) {
	panicStep := stepFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		})
	})
	p := pipeline.New(panicStep)
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	srv := httptest.NewServer(p.Handler(terminal))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPipeline_AbortHandlerRePropagates(t *testing.T) {
	abortStep := stepFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic(http.ErrAbortHandler)
		})
	})
	p := pipeline.New(abortStep)
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	handler := p.Handler(terminal)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(rec, req)
	}()

	if recovered != http.ErrAbortHandler {
		t.Errorf("recovered = %v, want http.ErrAbortHandler", recovered)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (no WriteHeader should fire on abort)", rec.Code, http.StatusOK)
	}
}

func TestPipeline_EmptyStepsCallsTerminalDirectly(t *testing.T) {
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	p := pipeline.New()
	srv := httptest.NewServer(p.Handler(terminal))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", resp.StatusCode)
	}
}

type stepFunc func(http.Handler) http.Handler

func (f stepFunc) Apply(next http.Handler) http.Handler { return f(next) }
