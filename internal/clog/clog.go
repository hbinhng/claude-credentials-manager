// Package clog routes diagnostic output to CCM_LOG_FILE when set.
//
// When CCM_LOG_FILE points to a writable path, Init opens it (append
// mode, 0600) and rewires:
//
//   - os.Stderr to the file (so every fmt.Fprintln(os.Stderr, ...)
//     and child-process stderr inheritance lands in the file)
//   - log.Default()'s output to the file (so log.Printf does too)
//
// Stdout is unaffected, so banners and tickets printed via fmt.Println
// stay on the user's terminal. Init must be called before any goroutine
// captures os.Stderr.
//
// On open failure (bad path, permission denied) Init prints a one-line
// warning to the original stderr and falls back to leaving everything
// on stderr — never aborts the running command.
package clog

import (
	"fmt"
	"log"
	"os"
)

// EnvVar is the env var name that controls log routing.
const EnvVar = "CCM_LOG_FILE"

// file holds the opened log file, or nil when CCM_LOG_FILE is unset
// or open failed.
var file *os.File

// origStderr remembers the pre-redirect stderr so warnings on open
// failure are visible to the user even though os.Stderr is being
// swapped.
var origStderr = os.Stderr

// Init opens the path in CCM_LOG_FILE (if set) and rewires log
// destinations. Idempotent: calling twice is safe (the second call is
// a no-op when the first call opened a file successfully).
func Init() {
	if file != nil {
		return
	}
	path := os.Getenv(EnvVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		fmt.Fprintf(origStderr, "ccm: cannot open %s=%q: %v (falling back to stderr)\n", EnvVar, path, err)
		return
	}
	file = f
	os.Stderr = f
	log.SetOutput(f)
}

// Close releases the log file. Safe to call when Init never opened
// one. Intended to be deferred from cmd.Execute().
func Close() {
	if file != nil {
		_ = file.Close()
		file = nil
		os.Stderr = origStderr
		log.SetOutput(origStderr)
	}
}
