//go:build windows

package share

// withLock is a no-op on Windows. ccm share and ccm launch are not a
// supported workflow on Windows; the mtime-reload path in credState
// still catches peer writes, but simultaneous refreshes across
// processes are not serialized.
func withLock(id string, fn func() error) error {
	return fn()
}
