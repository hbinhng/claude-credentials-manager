//go:build !windows

package cmd

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// freePort returns a TCP port that was available at the moment of
// the call.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestRunServe_IntegrationLoopback boots runServe in a goroutine on
// a loopback bind (so no auth), pings /healthz, then delivers
// SIGTERM via the process's own signal handler to trigger the
// graceful shutdown path. Covers the otherwise-integration-only
// body of runServe.
//
// Gated to Unix because syscall.Kill is not available on Windows.
func TestRunServe_IntegrationLoopback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	port := freePort(t)

	cmd := &cobra.Command{}
	cmd.Flags().String("bind-host", "127.0.0.1", "")
	cmd.Flags().String("bind-port", strconv.Itoa(port), "")

	done := make(chan error, 1)
	go func() { done <- runServe(cmd, nil) }()

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	var body []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if string(body) != "ok" {
		t.Fatalf("healthz body=%q, want ok", body)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServe returned %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatalf("runServe did not exit after SIGTERM")
	}

	if _, err := os.Stat(filepath.Join(home, ".ccm", "serve.pid")); !os.IsNotExist(err) {
		t.Errorf("pid file still present after shutdown: %v", err)
	}
}
