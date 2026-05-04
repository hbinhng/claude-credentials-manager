//go:build !windows

package claude

import (
	"errors"

	"github.com/zalando/go-keyring"
)

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
// credential blob in this host's keystore. See probeBackend for the
// rollout-aware decision tree that uses this signal.
func keychainHasClaudeEntry() bool {
	if keychainService == "" || keychainAccount == "" {
		return false
	}
	_, err := keyring.Get(keychainService, keychainAccount)
	return err == nil
}
