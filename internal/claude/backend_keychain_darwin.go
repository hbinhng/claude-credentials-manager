//go:build darwin

package claude

// Service and account names Claude Code uses for its OAuth blob in the
// macOS Keychain. The implementer must verify these against a real
// Claude Code install on macOS before this code ships.
//
// TODO(verify-on-mac): inspect the live keychain entry and substitute
// the observed strings.
const (
	keychainService = "Claude Code-credentials"
	keychainAccount = ""
)
