// Package transport internal tests cover branches that are only reachable
// from inside the package (nil-client guard, default-constant pin).
package transport

import "testing"

// TestDefaultProfileNameIsPinned guards against accidental drift of the
// verified profile constant. If you intentionally re-pin (e.g. after a
// fresh JA3 verification on a new bogdanfinn release), update the
// expected value here AND the package doc's probe table in transport.go.
//
// Per spec §7.4: re-verification on every bogdanfinn dependency bump is
// required.
func TestDefaultProfileNameIsPinned(t *testing.T) {
	const expected = "Firefox_135" // Tier B match per Task 1 verification gate
	if Default != expected {
		t.Fatalf("Default = %q, want %q. If this is intentional, re-pin Task 1's verification.", Default, expected)
	}
}

// TestTransport_Do_NilClient exercises the defensive nil-client guard.
// New() always sets client, so this branch is only reachable if a caller
// constructs Transport{} directly — which is unsupported but guarded.
func TestTransport_Do_NilClient(t *testing.T) {
	var tr Transport // zero value: client is nil
	_, err := tr.Do(nil)
	if err == nil {
		t.Error("expected error for nil client")
	}
}
