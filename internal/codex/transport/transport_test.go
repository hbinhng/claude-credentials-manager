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
