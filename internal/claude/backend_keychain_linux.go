//go:build linux

package claude

import "os/user"

// Linux uses the same service name and account convention as macOS.
// Claude Code uses npm `keytar`, which is a single cross-platform API
// over the host keystore (Keychain on macOS, Credential Manager on
// Windows, libsecret on Linux). keytar passes the SAME service+account
// strings through to every backend — so once Claude Code's Linux build
// starts writing to keytar, the entry will land in libsecret under
// service="Claude Code-credentials", account=<OS username>, identical
// to macOS today.
//
// zalando/go-keyring on Linux uses the same freedesktop generic schema
// (`org.freedesktop.Secret.Generic` with `service`/`account` attributes)
// keytar uses, so the two libraries are bit-compatible: ccm can read
// what keytar writes and vice versa.
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
