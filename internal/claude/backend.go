package claude

import "sync"

// backend is the storage adapter used by Active/Use/Sync/Restore. Both
// the file backend (~/.claude/.credentials.json) and the keychain
// backend (OS keystore) implement it.
type backend interface {
	// Read returns the current blob.
	//   - blob is the raw bytes (caller passes to decodeBlob).
	//   - ok is true when an entry exists; false when nothing is there.
	//   - err is non-nil only on real IO/keychain failures, never for
	//     "not found".
	Read() (blob []byte, ok bool, err error)
	Write(blob []byte) error
	Remove() error
}

// probeBackend returns keychainBackend when the OS keystore is reachable,
// fileBackend otherwise. Pulled out of currentBackend so tests can call
// it directly without relying on the cache.
func probeBackend() backend {
	if keychainProbe() {
		return keychainBackend{}
	}
	return fileBackend{}
}

// currentBackend returns the backend that should be used for this
// process. Reassigned in tests via withBackend(t, fakeBackend{}) or
// UseFileBackendForTest.
//
// The probe runs once per process and is cached. To force a re-probe
// in a test (e.g. after MockInit), reassign currentBackend directly.
var currentBackend = func() backend {
	probeOnce.Do(func() { cachedBackend = probeBackend() })
	return cachedBackend
}

var (
	probeOnce     sync.Once
	cachedBackend backend
)

// UseFileBackendForTest pins currentBackend to the file backend for the
// caller's scope and returns a restore function. Test-only seam — must
// not be called from production code. Lives in production package code
// (not a *_test.go file) so external test packages (e.g. cmd/) can use
// it; the leak is bounded by the package being internal/.
func UseFileBackendForTest() (restore func()) {
	orig := currentBackend
	currentBackend = func() backend { return fileBackend{} }
	return func() { currentBackend = orig }
}
