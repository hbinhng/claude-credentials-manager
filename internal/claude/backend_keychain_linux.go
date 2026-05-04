//go:build linux

package claude

// Service and account names Claude Code uses for its OAuth blob in the
// Linux Secret Service. The implementer must verify these by inspecting
// libsecret on a real Claude Code install:
//
//	secret-tool search --all
//
// before this code ships.
//
// TODO(verify-on-linux): substitute the observed service + account.
const (
	keychainService = "Claude Code-credentials"
	keychainAccount = ""
)
