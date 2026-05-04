//go:build windows

package claude

import (
	"errors"
	"testing"
)

// fakeWinStore replaces the production wincred seams with an in-memory
// map so unit tests don't touch the real Credential Manager. Each test
// installs one via withFakeWinStore() and the seams are restored in
// t.Cleanup.
type fakeWinStore struct {
	entries map[string][]byte
	users   map[string]string
	readErr error
	wrtErr  error
	delErr  error
}

func (f *fakeWinStore) read(target string) ([]byte, bool, error) {
	if f.readErr != nil {
		return nil, false, f.readErr
	}
	b, ok := f.entries[target]
	if !ok {
		return nil, false, nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, true, nil
}

func (f *fakeWinStore) write(target, user string, blob []byte) error {
	if f.wrtErr != nil {
		return f.wrtErr
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)
	f.entries[target] = cp
	f.users[target] = user
	return nil
}

func (f *fakeWinStore) delete(target string) error {
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.entries, target)
	delete(f.users, target)
	return nil
}

func withFakeWinStore(t *testing.T) *fakeWinStore {
	t.Helper()
	f := &fakeWinStore{entries: map[string][]byte{}, users: map[string]string{}}
	origRead, origWrite, origDelete := winRead, winWrite, winDelete
	winRead = f.read
	winWrite = f.write
	winDelete = f.delete
	t.Cleanup(func() {
		winRead = origRead
		winWrite = origWrite
		winDelete = origDelete
	})
	return f
}

func TestKeychainBackend_Windows_RoundTrip(t *testing.T) {
	f := withFakeWinStore(t)

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
	// Verify the target name uses keytar's "service/account" convention
	// — that's the whole reason this code path exists.
	if _, exists := f.entries[keychainService+"/"+keychainAccount]; !exists {
		t.Errorf("entry not at expected target name; entries=%v", f.entries)
	}
	// Verify UserName field is populated for Credential Manager UX.
	if u := f.users[keychainService+"/"+keychainAccount]; u != keychainAccount {
		t.Errorf("UserName = %q, want %q", u, keychainAccount)
	}
	if err := b.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := b.Read(); ok {
		t.Error("Read after Remove: ok=true")
	}
}

func TestKeychainBackend_Windows_Remove_MissingNoError(t *testing.T) {
	withFakeWinStore(t)
	if err := (keychainBackend{}).Remove(); err != nil {
		t.Errorf("Remove on empty store: %v, want nil", err)
	}
}

func TestKeychainBackend_Windows_ReadError_Propagates(t *testing.T) {
	f := withFakeWinStore(t)
	sentinel := errors.New("boom")
	f.readErr = sentinel
	if _, _, err := (keychainBackend{}).Read(); !errors.Is(err, sentinel) {
		t.Errorf("Read err = %v, want %v", err, sentinel)
	}
}

func TestKeychainBackend_Windows_WriteError_Propagates(t *testing.T) {
	f := withFakeWinStore(t)
	f.wrtErr = errors.New("write boom")
	if err := (keychainBackend{}).Write([]byte("x")); err == nil {
		t.Error("Write: nil err, want propagated")
	}
}

func TestKeychainBackend_Windows_DeleteError_Propagates(t *testing.T) {
	f := withFakeWinStore(t)
	if err := f.write(keychainService+"/"+keychainAccount, keychainAccount, []byte("x")); err != nil {
		t.Fatal(err)
	}
	f.delErr = errors.New("del boom")
	if err := (keychainBackend{}).Remove(); err == nil {
		t.Error("Remove: nil err, want propagated")
	}
}

func TestKeychainHasClaudeEntry_Windows_NoEntry(t *testing.T) {
	withFakeWinStore(t)
	if keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry on empty store = true, want false")
	}
}

func TestKeychainHasClaudeEntry_Windows_WithEntry(t *testing.T) {
	f := withFakeWinStore(t)
	if err := f.write(keychainService+"/"+keychainAccount, keychainAccount, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry after write = false, want true")
	}
}

func TestKeychainBackend_Windows_Unsupported_WhenAccountEmpty(t *testing.T) {
	withFakeWinStore(t)
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
