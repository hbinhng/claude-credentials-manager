package transport

import "testing"

func TestDefaultProfileNameIsPinned(t *testing.T) {
	if Default == "" {
		t.Fatal("Default profile name must be pinned (Task 1 verification gate)")
	}
}
