package claude

import (
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

// probeBackend rule 1: Claude has a keychain entry → keychain backend.
func TestProbeBackend_KeychainHasEntry_PicksKeychain(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	keyring.MockInit()
	if err := keyring.Set(keychainService, keychainAccount, "blob"); err != nil {
		t.Fatal(err)
	}
	got := probeBackend()
	if _, ok := got.(keychainBackend); !ok {
		t.Errorf("probeBackend = %T, want keychainBackend", got)
	}
}

// probeBackend rule 2: keychain empty, file present → file backend.
func TestProbeBackend_FileHasEntry_PicksFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	keyring.MockInit()
	if err := (fileBackend{}).Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	got := probeBackend()
	if _, ok := got.(fileBackend); !ok {
		t.Errorf("probeBackend = %T, want fileBackend", got)
	}
}

// probeBackend rule 3: nothing anywhere → platform default.
func TestProbeBackend_EmptyEverywhere_PlatformDefault(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	keyring.MockInit()
	got := probeBackend()
	wantKeychain := runtime.GOOS == "darwin"
	if wantKeychain {
		if _, ok := got.(keychainBackend); !ok {
			t.Errorf("probeBackend on darwin empty = %T, want keychainBackend", got)
		}
	} else {
		if _, ok := got.(fileBackend); !ok {
			t.Errorf("probeBackend on %s empty = %T, want fileBackend", runtime.GOOS, got)
		}
	}
}

// Transport down + file present → file. Transport down with no file
// hits the platform default branch — still safe because keychain
// methods will errUnsupported on Read.
func TestProbeBackend_TransportDown_FilePresent_PicksFile(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	keyring.MockInitWithError(errors.New("dbus down"))
	if err := (fileBackend{}).Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	got := probeBackend()
	if _, ok := got.(fileBackend); !ok {
		t.Errorf("probeBackend = %T, want fileBackend", got)
	}
}

func TestDefaultCurrentBackend_RunsProbeOnce(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	keyring.MockInit()
	if err := keyring.Set(keychainService, keychainAccount, "blob"); err != nil {
		t.Fatal(err)
	}
	probeOnce = sync.Once{}
	cachedBackend = nil
	t.Cleanup(func() {
		probeOnce = sync.Once{}
		cachedBackend = nil
	})

	first := defaultCurrentBackend()
	if _, ok := first.(keychainBackend); !ok {
		t.Errorf("first call: %T, want keychainBackend", first)
	}
	if got := defaultCurrentBackend(); got != first {
		t.Errorf("cache miss: got %T want same instance", got)
	}
}

func TestUseFileBackendForTest_OverridesAndRestores(t *testing.T) {
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
