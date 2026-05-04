//go:build windows

package claude

import (
	"errors"
	"os/user"

	"github.com/danieljoos/wincred"
)

// On Windows, Claude Code's keytar uses Windows Credential Manager
// generic credentials with target name = service + "/" + account.
// zalando/go-keyring uses ":" as the separator and isn't bit-compatible,
// so we drop down to wincred (a transitive dep already pulled in via
// go-keyring) and join with "/" to match keytar's schema.
const keychainService = "Claude Code-credentials"

// keychainAccount holds the per-user account string. Resolved at
// package init from os/user; empty when the lookup fails (unusual
// Windows state — falls back to errUnsupported, which routes the
// backend probe to the file backend).
var keychainAccount = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}()

// windowsTargetName builds the Credential Manager target string keytar
// uses on Windows: "<service>/<account>".
func windowsTargetName() string {
	return keychainService + "/" + keychainAccount
}

// Seams so tests can fake Windows Credential Manager I/O. Each holds
// the production wincred call by default; tests reassign these to
// in-memory implementations.
var (
	winRead   = realWinRead
	winWrite  = realWinWrite
	winDelete = realWinDelete
)

func realWinRead(target string) ([]byte, bool, error) {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	blob := make([]byte, len(cred.CredentialBlob))
	copy(blob, cred.CredentialBlob)
	return blob, true, nil
}

func realWinWrite(target, userName string, blob []byte) error {
	cred := wincred.NewGenericCredential(target)
	cred.CredentialBlob = blob
	cred.UserName = userName
	return cred.Write()
}

func realWinDelete(target string) error {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil
		}
		return err
	}
	return cred.Delete()
}

func (keychainBackend) Read() ([]byte, bool, error) {
	if keychainService == "" || keychainAccount == "" {
		return nil, false, errUnsupported
	}
	return winRead(windowsTargetName())
}

func (keychainBackend) Write(blob []byte) error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	return winWrite(windowsTargetName(), keychainAccount, blob)
}

func (keychainBackend) Remove() error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	return winDelete(windowsTargetName())
}

func keychainHasClaudeEntry() bool {
	if keychainService == "" || keychainAccount == "" {
		return false
	}
	_, ok, _ := winRead(windowsTargetName())
	return ok
}
