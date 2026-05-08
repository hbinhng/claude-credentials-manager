package transport_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/codex/transport"
)

func TestTransport_Do_RoundTripsBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "hello" {
			t.Errorf("upstream got body %q, want hello", body)
		}
		if got := r.Header.Get("X-Test"); got != "yes" {
			t.Errorf("upstream got X-Test=%q, want yes", got)
		}
		w.Header().Set("X-Reply", "ok")
		_, _ = io.WriteString(w, "world")
	}))
	defer srv.Close()

	tr, err := transport.New(transport.Options{
		ProfileName:        transport.Default,
		InsecureSkipVerify: true, // httptest cert
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("hello"))
	req.Header.Set("X-Test", "yes")
	resp, err := tr.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Reply"); got != "ok" {
		t.Errorf("X-Reply = %q, want ok", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Errorf("body = %q, want world", body)
	}
}

func TestTransport_Do_StreamingBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			io.WriteString(w, "data: "+string(rune('a'+i))+"\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	tr, _ := transport.New(transport.Options{
		ProfileName:        transport.Default,
		InsecureSkipVerify: true,
	})
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := tr.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "data: a") {
		t.Errorf("body missing first event: %q", body)
	}
	if !strings.Contains(string(body), "data: c") {
		t.Errorf("body missing third event: %q", body)
	}
}

func TestTransport_Do_ContextCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	tr, _ := transport.New(transport.Options{
		ProfileName:        transport.Default,
		InsecureSkipVerify: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	go func() { cancel() }()
	_, err := tr.Do(req)
	if err == nil {
		t.Error("Do should error on cancelled context")
	}
}

func TestTransport_New_UnknownProfile(t *testing.T) {
	_, err := transport.New(transport.Options{ProfileName: "DefinitelyNotAProfile"})
	if err == nil {
		t.Error("expected error for unknown profile")
	}
}

// TestTransport_New_EmptyProfileName verifies that an empty ProfileName
// falls back to the Default constant rather than erroring.
func TestTransport_New_EmptyProfileName(t *testing.T) {
	_, err := transport.New(transport.Options{}) // ProfileName == ""
	if err != nil {
		t.Fatalf("New with empty ProfileName: %v", err)
	}
}

// TestTransport_New_AllKnownProfiles exercises every case in lookupProfile
// to ensure the constant names match the profiles package variables.
func TestTransport_New_AllKnownProfiles(t *testing.T) {
	for _, name := range []string{
		"Chrome_120",
		"Chrome_131",
		"Chrome_133",
		"Firefox_120",
		"Firefox_123",
		"Firefox_132",
		"Firefox_135",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			_, err := transport.New(transport.Options{ProfileName: name})
			if err != nil {
				t.Fatalf("New(%q): %v", name, err)
			}
		})
	}
}

// TestTransport_New_ExplicitTimeout verifies that a non-zero Timeout is
// accepted without error.
func TestTransport_New_ExplicitTimeout(t *testing.T) {
	_, err := transport.New(transport.Options{
		ProfileName: transport.Default,
		Timeout:     30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New with explicit Timeout: %v", err)
	}
}

// TestTransport_Do_BadMethod verifies that an invalid HTTP method causes
// Do to return an error (exercises the fhttp.NewRequestWithContext error path).
// We construct the stdlib Request directly to bypass stdlib's method validation,
// then pass it to Do where fhttp's validator will reject the space in the method.
func TestTransport_Do_BadMethod(t *testing.T) {
	tr, err := transport.New(transport.Options{ProfileName: transport.Default})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	u, _ := url.Parse("https://example.com")
	// Build req manually to bypass stdlib's method check.
	req := &http.Request{
		Method: "INVALID METHOD", // space is invalid per RFC 7230 §3.2.6
		URL:    u,
		Header: http.Header{},
	}
	_, err = tr.Do(req)
	if err == nil {
		t.Error("Do with invalid method should return an error")
	}
}
