package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/share/middleware"
)

func TestCredSemaphore_LimitsConcurrency(t *testing.T) {
	mw := middleware.NewCredSemaphore(2)
	var inflight int32
	var maxSeen int32
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inflight, 1)
		for {
			old := atomic.LoadInt32(&maxSeen)
			if cur <= old || atomic.CompareAndSwapInt32(&maxSeen, old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
	})
	h := mw.Apply(terminal)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
		}()
	}
	wg.Wait()

	if maxSeen > 2 {
		t.Errorf("max in-flight = %d, want <= 2", maxSeen)
	}
}

func TestCredSemaphore_ZeroCapacityPassesThrough(t *testing.T) {
	mw := middleware.NewCredSemaphore(0)
	called := false
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	mw.Apply(terminal).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Error("zero-capacity should be no-op (no gating)")
	}
}

func TestCredSemaphore_ContextCancelReleases(t *testing.T) {
	mw := middleware.NewCredSemaphore(1)
	block := make(chan struct{})
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // hold the only slot
	})
	h := mw.Apply(terminal)

	go func() {
		req := httptest.NewRequest("GET", "/", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	time.Sleep(20 * time.Millisecond) // first request now holds the slot

	// Second request with already-cancelled context: should return promptly
	// instead of blocking on the saturated semaphore.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rr, req)
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(200 * time.Millisecond):
		t.Error("cancelled request did not return promptly")
	}
	close(block) // release first request
}
