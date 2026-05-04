//go:build windows

package claude

// Windows constants are unknown — every backend method short-circuits
// on the empty string and returns errUnsupported. Once the Credential
// Manager target name is verified, fill these in.
const (
	keychainService = ""
	keychainAccount = ""
)
