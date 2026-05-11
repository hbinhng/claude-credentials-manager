package clog

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// resetForTest restores package state so tests can run sequentially
// without state leaking between them. Tests cannot run in parallel
// because Init mutates the global os.Stderr.
func resetForTest(t *testing.T) {
	t.Helper()
	if file != nil {
		_ = file.Close()
		file = nil
	}
	os.Stderr = origStderr
	log.SetOutput(origStderr)
}

func TestInit_NoEnvVar_NoOp(t *testing.T) {
	resetForTest(t)
	t.Setenv(EnvVar, "")
	before := os.Stderr
	Init()
	if os.Stderr != before {
		t.Fatalf("os.Stderr was redirected with no env var")
	}
	if file != nil {
		t.Fatalf("file should be nil when env var unset")
	}
}

func TestInit_RoutesStderrAndLog(t *testing.T) {
	resetForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	t.Setenv(EnvVar, path)
	Init()
	defer Close()

	if os.Stderr == origStderr {
		t.Fatalf("os.Stderr was not redirected")
	}
	fmt.Fprintln(os.Stderr, "hello-stderr")
	log.Print("hello-log")

	// Force flush by closing — Close re-opens nothing, so we read after.
	Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Contains(data, []byte("hello-stderr")) {
		t.Errorf("stderr write not in log: %q", data)
	}
	if !bytes.Contains(data, []byte("hello-log")) {
		t.Errorf("log.Print not in log: %q", data)
	}
}

func TestInit_BadPath_FallsBackToStderr(t *testing.T) {
	resetForTest(t)
	// /dev/null/foo is unopenable on Unix (parent is char device).
	t.Setenv(EnvVar, "/dev/null/cannot-create-here.log")
	before := os.Stderr
	Init()
	if file != nil {
		t.Fatalf("file should be nil after open failure")
	}
	if os.Stderr != before {
		t.Fatalf("os.Stderr should not be redirected on open failure")
	}
}

func TestInit_Idempotent(t *testing.T) {
	resetForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "y.log")
	t.Setenv(EnvVar, path)
	Init()
	first := file
	Init()
	if file != first {
		t.Errorf("second Init should be a no-op; file pointer changed")
	}
	Close()
}

func TestClose_NoOpWhenNoFile(t *testing.T) {
	resetForTest(t)
	Close() // should not panic, should not touch os.Stderr
	if os.Stderr != origStderr {
		t.Errorf("Close on closed state shouldn't mutate os.Stderr")
	}
}

func TestClose_RestoresStderr(t *testing.T) {
	resetForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "z.log")
	t.Setenv(EnvVar, path)
	Init()
	if os.Stderr == origStderr {
		t.Fatalf("Init didn't redirect")
	}
	Close()
	if os.Stderr != origStderr {
		t.Errorf("Close didn't restore os.Stderr")
	}
}
