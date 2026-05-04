//go:build darwin

package claude

import (
	"errors"
	"testing"
)

// installFakeClaudeKeychainEntry plants a fake Claude entry via the
// secRead/secWrite seams. Same purpose as the Linux-side helper, but
// goes through the macOS-specific seam since /usr/bin/security
// lookups don't share state with go-keyring's mock.
func installFakeClaudeKeychainEntry(t *testing.T) {
	t.Helper()
	f := withFakeSecStore(t)
	if err := f.write(keychainService, keychainAccount, []byte("blob")); err != nil {
		t.Fatal(err)
	}
}

// resetKeychain ensures the fake macOS store has no Claude entry.
func resetKeychain(t *testing.T) {
	t.Helper()
	withFakeSecStore(t)
}

// breakKeychainTransport makes the fake store return an error from
// every read, simulating Keychain unreachable.
func breakKeychainTransport(t *testing.T) {
	t.Helper()
	f := withFakeSecStore(t)
	f.readErr = errors.New("keychain unreachable")
}
