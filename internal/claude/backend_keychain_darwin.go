//go:build darwin

package claude

import "os/user"

// On macOS, Claude Code stores its OAuth blob under service
// "Claude Code-credentials" with the OS login username as the account.
// Verified by inspecting `security find-generic-password -s "Claude Code-credentials"`
// on a live install: the `acct` attribute contains the macOS username.
const keychainService = "Claude Code-credentials"

// keychainAccount holds the per-user account string. Resolved once at
// package init from os/user; empty when the lookup fails (unusual
// macOS state — falls back to errUnsupported, which routes the backend
// probe to the file backend).
var keychainAccount = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}()
