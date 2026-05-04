//go:build linux

package claude

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeychainBackend_RoundTripWithMock(t *testing.T) {
	keyring.MockInit()
	b := keychainBackend{}

	if _, ok, err := b.Read(); err != nil || ok {
		t.Errorf("Read empty: ok=%v err=%v, want (false, nil)", ok, err)
	}
	if err := b.Write([]byte(`hello`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok, err := b.Read()
	if err != nil || !ok {
		t.Fatalf("Read after Write: ok=%v err=%v", ok, err)
	}
	if string(got) != "hello" {
		t.Errorf("Read = %q, want hello", got)
	}
	if err := b.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := b.Read(); ok {
		t.Error("Read after Remove: ok=true")
	}
}

func TestKeychainBackend_Remove_MissingNoError(t *testing.T) {
	keyring.MockInit()
	if err := (keychainBackend{}).Remove(); err != nil {
		t.Errorf("Remove on empty mock: %v, want nil", err)
	}
}

func TestKeychainBackend_ReadError_Propagates(t *testing.T) {
	sentinel := errors.New("boom")
	keyring.MockInitWithError(sentinel)
	_, ok, err := (keychainBackend{}).Read()
	if !errors.Is(err, sentinel) {
		t.Errorf("Read err = %v, want %v (errors.Is)", err, sentinel)
	}
	if ok {
		t.Error("ok = true on error path")
	}
}

func TestKeychainBackend_WriteError_Propagates(t *testing.T) {
	keyring.MockInitWithError(errors.New("write boom"))
	if err := (keychainBackend{}).Write([]byte(`x`)); err == nil {
		t.Error("Write: nil err, want propagated mock error")
	}
}

func TestKeychainBackend_RemoveError_Propagates(t *testing.T) {
	sentinel := errors.New("rm boom")
	keyring.MockInitWithError(sentinel)
	if err := (keychainBackend{}).Remove(); !errors.Is(err, sentinel) {
		t.Errorf("Remove err = %v, want %v", err, sentinel)
	}
}

func TestKeychainHasClaudeEntry_NoEntry(t *testing.T) {
	keyring.MockInit()
	if keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry on empty mock = true, want false")
	}
}

func TestKeychainHasClaudeEntry_WithEntry(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set(keychainService, keychainAccount, "blob"); err != nil {
		t.Fatal(err)
	}
	if !keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry after Set = false, want true")
	}
}

func TestKeychainHasClaudeEntry_TransportDown(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus down"))
	if keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry on broken transport = true, want false")
	}
}
