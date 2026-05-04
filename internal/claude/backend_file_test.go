package claude

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileBackend_ReadMissing(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	b := fileBackend{}
	blob, ok, err := b.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ok || blob != nil {
		t.Errorf("got (%q, %v), want (nil, false)", string(blob), ok)
	}
}

func TestFileBackend_RoundTrip(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	b := fileBackend{}
	want := []byte(`{"ccmSourceId":"x","claudeAiOauth":{}}`)
	if err := b.Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok, err := b.Read()
	if err != nil || !ok {
		t.Fatalf("Read after Write: ok=%v err=%v", ok, err)
	}
	if string(got) != string(want) {
		t.Errorf("Read = %q, want %q", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("perm = %o, want 0600", perm)
		}
	}
}

func TestFileBackend_Remove(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	b := fileBackend{}
	_ = b.Write([]byte(`x`))
	if err := b.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, ok, _ := b.Read()
	if ok {
		t.Error("Read after Remove: ok=true, want false")
	}
}

func TestFileBackend_Remove_MissingNoError(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	if err := (fileBackend{}).Remove(); err != nil {
		t.Errorf("Remove on missing file: %v, want nil", err)
	}
}

func TestFileBackend_WriteAtomic_NoTmpLeftover(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := (fileBackend{}).Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, ".credentials.json.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("leftover tmp file at %s", tmp)
	}
}

func TestFileBackend_WriteFailure(t *testing.T) {
	skipIfChmodNoOp(t)
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	if err := (fileBackend{}).Write([]byte(`{}`)); err == nil {
		t.Error("Write on read-only dir: nil err, want failure")
	}
}

func TestFileBackend_ReadOSError_Propagates(t *testing.T) {
	dir, cleanup := setupFakeHome(t)
	defer cleanup()

	target := filepath.Join(dir, ".credentials.json")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(target) })

	_, ok, err := (fileBackend{}).Read()
	if err == nil {
		t.Error("Read on a directory: nil err, want EISDIR")
	}
	if ok {
		t.Error("ok = true on error path")
	}
}
