//go:build windows

package claude

import (
	"errors"
	"testing"
)

func TestKeychainBackend_Windows_AllOpsReturnUnsupported(t *testing.T) {
	b := keychainBackend{}
	if _, _, err := b.Read(); !errors.Is(err, errUnsupported) {
		t.Errorf("Read err = %v, want errUnsupported", err)
	}
	if err := b.Write([]byte(`x`)); !errors.Is(err, errUnsupported) {
		t.Errorf("Write err = %v, want errUnsupported", err)
	}
	if err := b.Remove(); !errors.Is(err, errUnsupported) {
		t.Errorf("Remove err = %v, want errUnsupported", err)
	}
	if keychainHasClaudeEntry() {
		t.Error("keychainHasClaudeEntry = true on Windows stub, want false")
	}
}
