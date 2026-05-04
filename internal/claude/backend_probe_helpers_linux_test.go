//go:build linux

package claude

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// installFakeClaudeKeychainEntry plants a fake Claude entry so probe-
// based tests can assert "keychain has Claude" branches without
// touching the real OS keystore. Unix-side: uses go-keyring's mock.
func installFakeClaudeKeychainEntry(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	if err := keyring.Set(keychainService, keychainAccount, "blob"); err != nil {
		t.Fatal(err)
	}
}

// resetKeychain wipes any planted state for tests that need a clean
// keystore (probe must report "no Claude entry").
func resetKeychain(t *testing.T) {
	t.Helper()
	keyring.MockInit()
}

// breakKeychainTransport simulates a keystore transport failure. Used
// to verify probeBackend correctly classifies "broken" as "fall back
// to file".
func breakKeychainTransport(t *testing.T) {
	t.Helper()
	keyring.MockInitWithError(errors.New("transport down"))
}
