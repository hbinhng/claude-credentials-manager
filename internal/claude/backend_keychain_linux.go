//go:build linux

package claude

import "os/user"

// Linux side: Claude Code is assumed to use the same naming convention
// as macOS — service "Claude Code-credentials", account = OS username —
// because go-keyring's Linux backend talks Secret Service via libsecret
// and Claude's electron/keytar bindings tend to mirror the macOS schema.
//
// TODO(verify-on-linux): inspect a live Claude Code install on Linux
// (e.g. via `secret-tool search --all`) and confirm the schema. If the
// observed schema differs (different service name, different account
// attribute, or extra attributes required for libsecret to match),
// adjust accordingly.
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
