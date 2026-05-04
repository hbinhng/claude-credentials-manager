package claude

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// errUnsupported is returned by the keychain backend on platforms where
// we don't yet know the Claude Code identifiers (currently Windows).
var errUnsupported = errors.New("keychain backend not supported on this platform")

// keychainBackend talks to the OS keystore via go-keyring. The Claude
// service and account constants are platform-specific (see
// backend_keychain_*.go).
type keychainBackend struct{}

func (keychainBackend) Read() ([]byte, bool, error) {
	if keychainService == "" || keychainAccount == "" {
		return nil, false, errUnsupported
	}
	val, err := keyring.Get(keychainService, keychainAccount)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return []byte(val), true, nil
}

func (keychainBackend) Write(blob []byte) error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	return keyring.Set(keychainService, keychainAccount, string(blob))
}

func (keychainBackend) Remove() error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	if err := keyring.Delete(keychainService, keychainAccount); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// keychainHasClaudeEntry reports whether Claude Code currently stores a
// credential blob in this host's keystore. This is the signal for "use
// keychain backend" — purely "is the keystore reachable?" misfires
// during the per-platform rollout (e.g. Linux Claude Code 2.1.x has
// Secret Service available but still writes ~/.claude/.credentials.json).
func keychainHasClaudeEntry() bool {
	if keychainService == "" || keychainAccount == "" {
		return false
	}
	_, err := keyring.Get(keychainService, keychainAccount)
	return err == nil
}
