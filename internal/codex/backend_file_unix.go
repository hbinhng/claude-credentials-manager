//go:build !windows

package codex

import (
	"fmt"
	"os"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Write replaces ~/.codex/auth.json with a symlink pointing at the
// ccm-stored credential file identified by the blob's `id` key.
func (fileBackend) Write(blob []byte) error {
	id, err := decodeBlob(blob)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("codex: cannot determine source id from blob")
	}
	source := store.CredPath(id)
	_ = os.Remove(authPath())
	if err := os.Symlink(source, authPath()); err != nil {
		return fmt.Errorf("codex: symlink auth.json: %w", err)
	}
	return nil
}
