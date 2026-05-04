//go:build darwin

package claude

import (
	"errors"
	"os/exec"
	"os/user"
	"strings"
)

// On macOS, ccm bypasses zalando/go-keyring and shells out to
// /usr/bin/security directly. Reason: go-keyring v0.2.x wraps every
// macOS value with a `go-keyring-base64:` prefix and base64-encodes
// the payload — its own framing scheme to handle keychain length
// limits and binary data. Claude Code's keytar uses the lower-level
// SecItemAdd C API and stores raw bytes. The two are not bit-
// compatible: a value written by go-keyring looks like garbage to
// keytar, and Claude can't read its own credentials after ccm touches
// the entry.
//
// We use the `security` CLI directly with the `-w` flag, matching what
// keytar effectively does. Tests stub the seams (secRead/secWrite/
// secDelete) so unit tests don't touch the real Keychain.
//
// On macOS Claude Code 2.1.x has fully migrated to keychain — entry is
// at service "Claude Code-credentials", account = OS login username,
// password = the JSON credential blob.
const keychainService = "Claude Code-credentials"

var keychainAccount = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}()

// security exit code 44 = errSecItemNotFound. We map this to the
// "ok=false, err=nil" contract so probeBackend can distinguish "no
// Claude entry" from "keychain unreachable".
const secNotFoundExitCode = 44

var (
	secRead   = realSecRead
	secWrite  = realSecWrite
	secDelete = realSecDelete
)

func realSecRead(service, account string) ([]byte, bool, error) {
	out, err := exec.Command("/usr/bin/security",
		"find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == secNotFoundExitCode {
			return nil, false, nil
		}
		return nil, false, err
	}
	// security appends a trailing newline; strip it so the bytes are
	// what was originally stored.
	return []byte(strings.TrimRight(string(out), "\n")), true, nil
}

func realSecWrite(service, account string, blob []byte) error {
	// Delete first so a fresh entry replaces any prior attributes.
	// We swallow the not-found error here.
	if err := exec.Command("/usr/bin/security",
		"delete-generic-password", "-s", service, "-a", account).Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != secNotFoundExitCode {
			return err
		}
	}
	return exec.Command("/usr/bin/security",
		"add-generic-password", "-s", service, "-a", account, "-w", string(blob)).Run()
}

func realSecDelete(service, account string) error {
	err := exec.Command("/usr/bin/security",
		"delete-generic-password", "-s", service, "-a", account).Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == secNotFoundExitCode {
			return nil
		}
		return err
	}
	return nil
}

func (keychainBackend) Read() ([]byte, bool, error) {
	if keychainService == "" || keychainAccount == "" {
		return nil, false, errUnsupported
	}
	return secRead(keychainService, keychainAccount)
}

func (keychainBackend) Write(blob []byte) error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	return secWrite(keychainService, keychainAccount, blob)
}

func (keychainBackend) Remove() error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	return secDelete(keychainService, keychainAccount)
}

func keychainHasClaudeEntry() bool {
	if keychainService == "" || keychainAccount == "" {
		return false
	}
	_, ok, _ := secRead(keychainService, keychainAccount)
	return ok
}
