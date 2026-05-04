//go:build windows

package claude

// Windows is intentionally a stub. Claude Code's keytar uses Windows
// Credential Manager with target name = service + "/" + account, while
// zalando/go-keyring (the Linux/macOS lib we depend on) joins them
// with ":". The two libraries are NOT bit-compatible on Windows — go-
// keyring would write entries keytar can't find and vice versa.
//
// Until ccm switches to a Windows backend that matches keytar's target-
// name convention (e.g. github.com/danieljoos/wincred directly), every
// keychainBackend method short-circuits to errUnsupported via the empty
// constants and probeBackend falls through to fileBackend on Windows.
//
// Claude Code Windows is still on the file backend as of 2.1.x anyway,
// so this stub is functionally invisible to current users.
const (
	keychainService = ""
	keychainAccount = ""
)
