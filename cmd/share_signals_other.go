//go:build windows

package cmd

// On Windows, SIGUSR1 does not exist; the snapshot signal is a
// no-op. Operators can still see rotation logs on stderr.
