//go:build darwin

package claude

import (
	"errors"
	"testing"
)

// fakeSecStore replaces the production security-CLI seams with an in-
// memory map so unit tests don't touch the real Keychain. Each test
// installs one via withFakeSecStore() and the seams are restored in
// t.Cleanup.
type fakeSecStore struct {
	entries map[string][]byte
	readErr error
	wrtErr  error
	delErr  error
}

func (f *fakeSecStore) read(service, account string) ([]byte, bool, error) {
	if f.readErr != nil {
		return nil, false, f.readErr
	}
	b, ok := f.entries[service+"/"+account]
	if !ok {
		return nil, false, nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, true, nil
}

func (f *fakeSecStore) write(service, account string, blob []byte) error {
	if f.wrtErr != nil {
		return f.wrtErr
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)
	f.entries[service+"/"+account] = cp
	return nil
}

func (f *fakeSecStore) delete(service, account string) error {
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.entries, service+"/"+account)
	return nil
}

func withFakeSecStore(t *testing.T) *fakeSecStore {
	t.Helper()
	f := &fakeSecStore{entries: map[string][]byte{}}
	origRead, origWrite, origDelete := secRead, secWrite, secDelete
	secRead = f.read
	secWrite = f.write
	secDelete = f.delete
	t.Cleanup(func() {
		secRead = origRead
		secWrite = origWrite
		secDelete = origDelete
	})
	return f
}

func TestKeychainBackend_Darwin_RoundTrip(t *testing.T) {
	withFakeSecStore(t)

	b := keychainBackend{}
	if _, ok, err := b.Read(); err != nil || ok {
		t.Errorf("Read empty: ok=%v err=%v, want (false, nil)", ok, err)
	}
	if err := b.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok, err := b.Read()
	if err != nil || !ok || string(got) != "hi" {
		t.Errorf("Read after Write: got=%q ok=%v err=%v", got, ok, err)
	}
	if err := b.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := b.Read(); ok {
		t.Error("Read after Remove: ok=true")
	}
}

func TestKeychainBackend_Darwin_Remove_MissingNoError(t *testing.T) {
	withFakeSecStore(t)
	if err := (keychainBackend{}).Remove(); err != nil {
		t.Errorf("Remove on empty store: %v, want nil", err)
	}
}

func TestKeychainBackend_Darwin_ReadError_Propagates(t *testing.T) {
	f := withFakeSecStore(t)
	sentinel := errors.New("boom")
	f.readErr = sentinel
	if _, _, err := (keychainBackend{}).Read(); !errors.Is(err, sentinel) {
		t.Errorf("Read err = %v, want %v", err, sentinel)
	}
}

func TestKeychainBackend_Darwin_WriteError_Propagates(t *testing.T) {
	f := withFakeSecStore(t)
	f.wrtErr = errors.New("write boom")
	if err := (keychainBackend{}).Write([]byte("x")); err == nil {
		t.Error("Write: nil err, want propagated")
	}
}

func TestKeychainBackend_Darwin_DeleteError_Propagates(t *testing.T) {
	f := withFakeSecStore(t)
	if err := f.write(keychainService, keychainAccount, []byte("x")); err != nil {
		t.Fatal(err)
	}
	f.delErr = errors.New("del boom")
	if err := (keychainBackend{}).Remove(); err == nil {
		t.Error("Remove: nil err, want propagated")
	}
}

func TestKeychainHasClaudeEntry_Darwin_NoEntry(t *testing.T) {
	withFakeSecStore(t)
	if keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry on empty store = true, want false")
	}
}

func TestKeychainHasClaudeEntry_Darwin_WithEntry(t *testing.T) {
	f := withFakeSecStore(t)
	if err := f.write(keychainService, keychainAccount, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry after write = false, want true")
	}
}

func TestKeychainBackend_Darwin_Unsupported_WhenAccountEmpty(t *testing.T) {
	withFakeSecStore(t)
	orig := keychainAccount
	keychainAccount = ""
	t.Cleanup(func() { keychainAccount = orig })

	b := keychainBackend{}
	if _, _, err := b.Read(); !errors.Is(err, errUnsupported) {
		t.Errorf("Read err = %v, want errUnsupported", err)
	}
	if err := b.Write([]byte("x")); !errors.Is(err, errUnsupported) {
		t.Errorf("Write err = %v, want errUnsupported", err)
	}
	if err := b.Remove(); !errors.Is(err, errUnsupported) {
		t.Errorf("Remove err = %v, want errUnsupported", err)
	}
	if keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry with empty account = true, want false")
	}
}
