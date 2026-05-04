package claude

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// errUnsupported is returned by the keychain backend on platforms where
// we don't yet know the Claude Code identifiers (currently Windows).
var errUnsupported = errors.New("keychain backend not supported on this platform")

// keychainBackend talks to the OS keystore via go-keyring. The Claude
// service and account constants are platform-specific (see
// backend_keychain_*.go). Probing uses a separate sentinel service so
// the probe tests transport health, not the presence of a real Claude
// entry — otherwise an unverified/wrong account string would make the
// probe pass with ErrNotFound and Read calls would silently return
// "no entry" forever.
type keychainBackend struct{}

const (
	probeService = "ccm-keychain-probe"
	probeAccount = "probe"
)

func (keychainBackend) Read() ([]byte, bool, error) {
	if keychainService == "" || keychainAccount == "" {
		return nil, false, errUnsupported
	}
	val, err := keyring.Get(keychainService, keychainAccount)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return []byte(val), true, nil
}

func (keychainBackend) Write(blob []byte) error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	return keyring.Set(keychainService, keychainAccount, string(blob))
}

func (keychainBackend) Remove() error {
	if keychainService == "" || keychainAccount == "" {
		return errUnsupported
	}
	if err := keyring.Delete(keychainService, keychainAccount); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// keychainProbe checks whether the OS keystore is reachable by
// attempting a Get on a sentinel service ccm controls. Classifies:
//   - nil err          → keychain works AND we somehow have a probe
//                        entry, return true.
//   - ErrNotFound      → keychain works, no probe entry, return true.
//   - any other err    → keychain transport down, return false.
//
// Using a sentinel rather than the Claude service/account avoids
// confusing "Claude entry missing" with "keychain unreachable".
func keychainProbe() bool {
	if keychainService == "" || keychainAccount == "" {
		return false
	}
	_, err := keyring.Get(probeService, probeAccount)
	if err == nil {
		return true
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return true
	}
	return false
}
