//go:build linux

package claude

import (
	"errors"
	"os/user"

	"github.com/zalando/go-keyring"
)

// Linux uses the same service name and account convention as macOS,
// but a different access path (libsecret/Secret Service via D-Bus
// rather than the macOS Keychain). Claude Code uses npm `keytar`,
// which writes to libsecret with the freedesktop generic schema
// (`org.freedesktop.Secret.Generic` with `service`/`account`
// attributes). zalando/go-keyring uses the same schema, so the two
// libraries are bit-compatible on Linux — unlike macOS, where
// go-keyring wraps values with a `go-keyring-base64:` prefix that
// keytar can't decode (so darwin uses /usr/bin/security directly).
//
// As of Claude Code 2.1.x (May 2026), the Linux build is still on the
// file backend, so this code path is dormant; probeBackend correctly
// picks the file backend until Claude flips. No further verification
// needed when that flip happens.
const keychainService = "Claude Code-credentials"

// keychainAccount holds the per-user account string. Resolved once at
// package init from os/user; empty when the lookup fails (e.g. inside
// a container with no /etc/passwd entry — falls back to errUnsupported,
// which routes the backend probe to the file backend).
var keychainAccount = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}()

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

func keychainHasClaudeEntry() bool {
	if keychainService == "" || keychainAccount == "" {
		return false
	}
	_, err := keyring.Get(keychainService, keychainAccount)
	return err == nil
}
