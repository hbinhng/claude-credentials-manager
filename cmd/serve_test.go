package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveToken_LoopbackReturnsEmpty(t *testing.T) {
	t.Setenv("CCM_SERVE_TOKEN", "abcdefghijklmnop")
	tok, err := resolveToken(true)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if tok != "" {
		t.Errorf("tok=%q, want empty in loopback", tok)
	}
}

func TestResolveToken_UsesCCMServeTokenWhenSet(t *testing.T) {
	t.Setenv("CCM_SERVE_TOKEN", strings.Repeat("x", 20))
	tok, err := resolveToken(false)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if tok != strings.Repeat("x", 20) {
		t.Errorf("tok=%q, want xxx…x", tok)
	}
}

func TestResolveToken_RejectsShortToken(t *testing.T) {
	t.Setenv("CCM_SERVE_TOKEN", "short")
	_, err := resolveToken(false)
	if err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("err=%v, want 'at least 16'", err)
	}
}

func TestResolveToken_GeneratesRandomTokenWhenUnset(t *testing.T) {
	t.Setenv("CCM_SERVE_TOKEN", "")
	tok, err := resolveToken(false)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if len(tok) < 20 {
		t.Errorf("random token too short: %q", tok)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{"", "127.0.0.1", "::1", "localhost"}
	nonLoopback := []string{"0.0.0.0", "192.168.1.5", "::", "example.com"}
	for _, h := range loopback {
		if !isLoopbackHost(h) {
			t.Errorf("%q classified as non-loopback; want loopback", h)
		}
	}
	for _, h := range nonLoopback {
		if isLoopbackHost(h) {
			t.Errorf("%q classified as loopback; want non-loopback", h)
		}
	}
}

func TestEffectiveHost(t *testing.T) {
	if got := effectiveHost(""); got != "127.0.0.1" {
		t.Errorf("effectiveHost(\"\") = %q, want 127.0.0.1", got)
	}
	if got := effectiveHost("0.0.0.0"); got != "0.0.0.0" {
		t.Errorf("effectiveHost(0.0.0.0) = %q, want unchanged", got)
	}
	if got := effectiveHost("192.168.1.5"); got != "192.168.1.5" {
		t.Errorf("effectiveHost(192.168.1.5) = %q, want unchanged", got)
	}
}

func TestPIDFileLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := pidFilePath()
	if err := writePIDFile(path); err != nil {
		t.Fatalf("writePIDFile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("pid file not created: %v", err)
	}

	if err := writePIDFile(path); err == nil {
		t.Errorf("writePIDFile succeeded; want 'already running'")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Errorf("err=%v, want 'already running'", err)
	}

	_ = removePIDFile(path)
	if err := writePIDFile(path); err != nil {
		t.Errorf("writePIDFile after remove: %v", err)
	}
	_ = removePIDFile(path)
}

func TestWritePIDFile_StaleOverwritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := pidFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("999999"), 0644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	if err := writePIDFile(path); err != nil {
		t.Errorf("writePIDFile should overwrite stale PID: %v", err)
	}
	_ = removePIDFile(path)
}

func TestWritePIDFile_IgnoresGarbageContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := pidFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not-a-pid"), 0644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if err := writePIDFile(path); err != nil {
		t.Errorf("writePIDFile should overwrite garbage content: %v", err)
	}
	_ = removePIDFile(path)
}

func TestPrintServeBanner_LoopbackOmitsToken(t *testing.T) {
	out := captureStdout(t, func() {
		printServeBanner("127.0.0.1:7878", "unused", true)
	})
	if !strings.Contains(out, "url:    http://127.0.0.1:7878") {
		t.Errorf("missing url line: %s", out)
	}
	if strings.Contains(out, "token:") {
		t.Errorf("loopback banner leaked token: %s", out)
	}
	if strings.Contains(out, "open:") {
		t.Errorf("loopback banner leaked open URL: %s", out)
	}
}

func TestPrintServeBanner_NonLoopbackIncludesToken(t *testing.T) {
	out := captureStdout(t, func() {
		printServeBanner("0.0.0.0:7878", "abc123", false)
	})
	if !strings.Contains(out, "token:  abc123") {
		t.Errorf("missing token: %s", out)
	}
	if !strings.Contains(out, "open:   http://0.0.0.0:7878/?token=abc123") {
		t.Errorf("missing open URL: %s", out)
	}
}

// captureStdout redirects os.Stdout, runs fn, and returns what was
// written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}
