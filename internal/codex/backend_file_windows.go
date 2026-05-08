//go:build windows

package codex

import (
	"encoding/json"
	"fmt"
	"os"
)

// Write writes a wrapper-copy of the codex credential blob with a
// ccmSourceId marker. Windows symlinks require admin/Dev Mode.
func (fileBackend) Write(blob []byte) error {
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		return fmt.Errorf("codex: decode blob: %w", err)
	}
	if id, _ := m["id"].(string); id != "" {
		m["ccmSourceId"] = id
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(authPath(), out, 0o600)
}
