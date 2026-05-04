package claude

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

// currentBackend returns the backend that should be used for this
// process. Reassigned in tests via withBackend(t, fakeBackend{}).
//
// Until Task 6 wires in the real probe, this just returns fileBackend
// so existing behavior is preserved.
var currentBackend = func() backend { return fileBackend{} }
