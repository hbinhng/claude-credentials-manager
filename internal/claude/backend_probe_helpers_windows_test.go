//go:build windows

package claude

import (
	"errors"
	"testing"
)

// installFakeClaudeKeychainEntry plants a fake Claude entry via the
// winRead/winWrite seams. Same purpose as the Unix-side helper, but
// goes through the Windows-specific seam since wincred lookups don't
// share state with go-keyring's mock.
func installFakeClaudeKeychainEntry(t *testing.T) {
	t.Helper()
	f := withFakeWinStore(t)
	if err := f.write(windowsTargetName(), keychainAccount, []byte("blob")); err != nil {
		t.Fatal(err)
	}
}

// resetKeychain ensures the fake Windows store has no Claude entry.
// Installs an empty fake (returns "not found" for any read).
func resetKeychain(t *testing.T) {
	t.Helper()
	withFakeWinStore(t)
}

// breakKeychainTransport makes the fake Windows store return an error
// from every read, simulating Credential Manager unavailability.
func breakKeychainTransport(t *testing.T) {
	t.Helper()
	f := withFakeWinStore(t)
	f.readErr = errors.New("transport down")
}
