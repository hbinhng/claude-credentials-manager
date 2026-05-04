package claude

import "testing"

// fakeBackend is an in-memory backend used to exercise higher-level ops
// (Use/Sync/Restore/Active) without touching the filesystem or a real
// keychain. WriteErr/ReadErr/RemoveErr can be set to force failures.
type fakeBackend struct {
	blob      []byte
	exists    bool
	WriteErr  error
	ReadErr   error
	RemoveErr error
}

func (f *fakeBackend) Read() ([]byte, bool, error) {
	if f.ReadErr != nil {
		return nil, false, f.ReadErr
	}
	if !f.exists {
		return nil, false, nil
	}
	cp := make([]byte, len(f.blob))
	copy(cp, f.blob)
	return cp, true, nil
}

func (f *fakeBackend) Write(blob []byte) error {
	if f.WriteErr != nil {
		return f.WriteErr
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)
	f.blob = cp
	f.exists = true
	return nil
}

func (f *fakeBackend) Remove() error {
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	f.blob = nil
	f.exists = false
	return nil
}

// withBackend installs fb as currentBackend for the duration of the test.
func withBackend(t *testing.T, fb backend) {
	t.Helper()
	orig := currentBackend
	currentBackend = func() backend { return fb }
	t.Cleanup(func() { currentBackend = orig })
}

func TestFakeBackend_RoundTrip(t *testing.T) {
	fb := &fakeBackend{}
	if _, ok, _ := fb.Read(); ok {
		t.Error("fresh fakeBackend Read: ok=true, want false")
	}
	if err := fb.Write([]byte(`hi`)); err != nil {
		t.Fatal(err)
	}
	blob, ok, _ := fb.Read()
	if !ok || string(blob) != "hi" {
		t.Errorf("Read = (%q, %v), want (\"hi\", true)", blob, ok)
	}
	if err := fb.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := fb.Read(); ok {
		t.Error("Read after Remove: ok=true")
	}
}
