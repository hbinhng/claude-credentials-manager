package codexoauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultListenAddr is the registered OAuth redirect host:port.
const DefaultListenAddr = "127.0.0.1:1455"

// DefaultRedirectURI matches what's registered on the OpenAI OAuth app.
const DefaultRedirectURI = "http://localhost:1455/auth/callback"

// ListenAddr is the address StartCallbackServer binds to. Tests
// override (e.g., to "127.0.0.1:0" for an ephemeral port).
var ListenAddr = DefaultListenAddr

type CallbackServer struct {
	srv      *http.Server
	addr     string
	expState string

	mu        sync.Mutex
	delivered bool
	result    chan callbackResult
	shut      chan struct{}
}

type callbackResult struct {
	code string
	err  error
}

// StartCallbackServer binds the listener and returns the server,
// its bound address (host:port), and any startup error.
func StartCallbackServer(expState string) (*CallbackServer, string, error) {
	ln, err := net.Listen("tcp", ListenAddr)
	if err != nil {
		if isAddrInUse(err) {
			return nil, "", ErrPortInUse
		}
		return nil, "", fmt.Errorf("codexoauth: bind callback listener: %w", err)
	}
	c := &CallbackServer{
		expState: expState,
		addr:     ln.Addr().String(),
		result:   make(chan callbackResult, 1),
		shut:     make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", c.handle)
	c.srv = &http.Server{Handler: mux}
	go c.srv.Serve(ln) //nolint:errcheck // returns http.ErrServerClosed on Shutdown
	return c, c.addr, nil
}

// isAddrInUse reports whether err is an "address already in use" error.
// The Windows variant ("Only one usage of each socket") is covered by
// the same string-match; the nil guard is intentionally absent because
// this is only ever called with a non-nil error from net.Listen.
func isAddrInUse(err error) bool {
	s := err.Error()
	return strings.Contains(s, "address already in use") || strings.Contains(s, "Only one usage of each socket")
}

func (c *CallbackServer) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	errParam := q.Get("error")
	code := q.Get("code")

	respond := func(msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body style="font:14px sans-serif;text-align:center;padding:3em">%s</body></html>`, msg)
	}

	c.mu.Lock()
	if c.delivered {
		c.mu.Unlock()
		respond("You can close this tab.")
		return
	}
	c.delivered = true
	c.mu.Unlock()

	switch {
	case errParam == "access_denied":
		respond("Login canceled. You can close this tab.")
		c.result <- callbackResult{err: ErrAuthDenied}
	case errParam != "":
		desc := q.Get("error_description")
		if desc == "" {
			desc = errParam
		}
		respond("OpenAI returned an error. You can close this tab.")
		c.result <- callbackResult{err: fmt.Errorf("%w: %s", ErrTokenEndpoint, desc)}
	case state != c.expState:
		respond("State mismatch. You can close this tab.")
		c.result <- callbackResult{err: ErrStateMismatch}
	case code == "":
		respond("Missing code. You can close this tab.")
		c.result <- callbackResult{err: errors.New("codexoauth: callback missing code")}
	default:
		respond("You can close this tab.")
		c.result <- callbackResult{code: code}
	}
}

// Wait blocks for at most d for a callback. Returns the auth code on
// success, or a typed error.
func (c *CallbackServer) Wait(d time.Duration) (string, error) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-c.result:
		return r.code, r.err
	case <-timer.C:
		return "", ErrCallbackTimeout
	}
}

// Shutdown stops the HTTP server. Safe to call multiple times.
func (c *CallbackServer) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	select {
	case <-c.shut:
		c.mu.Unlock()
		return nil
	default:
		close(c.shut)
	}
	c.mu.Unlock()
	return c.srv.Shutdown(ctx)
}
