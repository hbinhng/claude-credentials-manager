package claude

import (
	"errors"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestProbeBackend_KeychainHealthy_PicksKeychain(t *testing.T) {
	keyring.MockInit()
	got := probeBackend()
	if _, ok := got.(keychainBackend); !ok {
		t.Errorf("probeBackend = %T, want keychainBackend", got)
	}
}

func TestProbeBackend_KeychainBroken_FallsBackToFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus down"))
	got := probeBackend()
	if _, ok := got.(fileBackend); !ok {
		t.Errorf("probeBackend = %T, want fileBackend", got)
	}
}

func TestUseFileBackendForTest_OverridesAndRestores(t *testing.T) {
	// Force a known starting state: keychain backend via mock.
	keyring.MockInit()
	probeOnce = sync.Once{}
	cachedBackend = nil
	// Override globally to ensure baseline isn't already fileBackend.
	currentBackend = func() backend { return keychainBackend{} }

	if _, ok := currentBackend().(keychainBackend); !ok {
		t.Fatal("setup invariant: expected keychainBackend baseline")
	}

	restore := UseFileBackendForTest()
	if _, ok := currentBackend().(fileBackend); !ok {
		t.Errorf("after override, currentBackend = %T, want fileBackend", currentBackend())
	}

	restore()
	if _, ok := currentBackend().(keychainBackend); !ok {
		t.Errorf("after restore, currentBackend = %T, want keychainBackend", currentBackend())
	}
}
