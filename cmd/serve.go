package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/serve"
	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("bind-host", "", "literal listener bind address (empty = 127.0.0.1)")
	serveCmd.Flags().String("bind-port", "7878", "port to bind")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run a local HTTP dashboard for managing ccm share sessions",
	Long: `Run a long-running local HTTP dashboard that manages multiple
concurrent ccm share sessions in-process.

With no flags the server binds 127.0.0.1:7878 and requires no
authentication (loopback is the trust boundary). Any non-loopback
--bind-host activates an admin token: CCM_SERVE_TOKEN if set
(minimum 16 chars), otherwise a random 22-char URL-safe token is
generated and printed once at startup. The token is accepted via
Authorization: Bearer, a ccm_serve_token cookie, or a ?token=
query parameter.

Only one ccm serve runs at a time (enforced via ~/.ccm/serve.pid
with stale-PID detection). Ctrl-C tears every managed session
down cleanly before exiting.`,
	RunE: runServe,
}

func runServe(cmd *cobra.Command, _ []string) error {
	// coverage: integration
	bindHost, _ := cmd.Flags().GetString("bind-host")
	bindPortStr, _ := cmd.Flags().GetString("bind-port")
	bindPort, err := strconv.Atoi(bindPortStr)
	if err != nil || bindPort < 1 || bindPort > 65535 {
		return fmt.Errorf("invalid --bind-port %q", bindPortStr)
	}

	loopback := isLoopbackHost(bindHost)
	token, err := resolveToken(loopback)
	if err != nil {
		return err
	}

	pidPath := pidFilePath()
	if err := writePIDFile(pidPath); err != nil {
		return err
	}
	defer removePIDFile(pidPath)

	mgr := serve.NewManager(share.DefaultStarter, os.Stderr)
	handler, err := serve.NewHandler(serve.ServerConfig{
		Manager:  mgr,
		Token:    token,
		Loopback: loopback,
	})
	if err != nil {
		return err
	}

	listenAddr := net.JoinHostPort(effectiveHost(bindHost), strconv.Itoa(bindPort))
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{Handler: handler}

	printServeBanner(ln.Addr().String(), token, loopback)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	mgrErr := mgr.Shutdown(ctx)
	srvErr := srv.Shutdown(ctx)
	return errors.Join(mgrErr, srvErr)
}

// isLoopbackHost returns true when the --bind-host value resolves to
// loopback-only (and thus auth can be bypassed).
func isLoopbackHost(h string) bool {
	switch h {
	case "", "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// effectiveHost maps an empty --bind-host to 127.0.0.1; otherwise the
// string is returned unchanged and fed directly into net.Listen.
func effectiveHost(h string) string {
	if h == "" {
		return "127.0.0.1"
	}
	return h
}

// resolveToken returns the admin token to enforce on non-loopback
// binds. Loopback binds return "" (no auth enforced). Reads
// CCM_SERVE_TOKEN if set (must be ≥16 chars), otherwise generates a
// random 128-bit token encoded with RawURLEncoding.
func resolveToken(loopback bool) (string, error) {
	if loopback {
		return "", nil
	}
	if env := os.Getenv("CCM_SERVE_TOKEN"); env != "" {
		if len(env) < 16 {
			return "", errors.New("CCM_SERVE_TOKEN must be at least 16 characters")
		}
		return env, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// coverage: unreachable — crypto/rand only errors on a kernel
		// RNG failure, which is not exercisable in tests.
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pidFilePath is the canonical location of the serve PID file.
func pidFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccm", "serve.pid")
}

// writePIDFile writes our PID to path. Refuses to overwrite if the
// existing file points at a still-alive PID. Stale PIDs are silently
// replaced.
func writePIDFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if raw, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			if pidAlive(pid) {
				return fmt.Errorf("ccm serve already running (pid %d); kill it first or check %s", pid, path)
			}
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)
}

// removePIDFile deletes the PID file. Errors (e.g. the file was
// already removed by another process) are ignored by the caller.
func removePIDFile(path string) error { return os.Remove(path) }

// printServeBanner writes the startup banner to stdout. On loopback,
// the token + open lines are omitted.
func printServeBanner(addr, token string, loopback bool) {
	fmt.Printf("ccm serve %s is live.\n", Version)
	fmt.Printf("  url:    http://%s\n", addr)
	if !loopback {
		fmt.Printf("  token:  %s\n", token)
		fmt.Printf("  open:   http://%s/?token=%s\n", addr, token)
	}
	fmt.Println("Press Ctrl-C to stop (tears down all managed sessions).")
}
