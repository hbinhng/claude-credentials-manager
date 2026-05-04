package claude

import "errors"

// errUnsupported is returned by the keychain backend when platform-
// specific identifiers haven't been resolved (e.g. user.Current() fails
// inside a stripped container, leaving keychainAccount empty). Routes
// the probe to the file backend.
var errUnsupported = errors.New("keychain backend not supported on this platform")

// keychainBackend talks to the OS keystore. Per-platform implementations
// live in backend_keychain_unix.go (zalando/go-keyring on macOS+Linux,
// bit-compatible with keytar's freedesktop-generic schema) and
// backend_keychain_windows.go (wincred directly with target name
// "service/account" to match keytar's Windows schema).
type keychainBackend struct{}
