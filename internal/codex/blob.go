package codex

import (
	"encoding/json"
	"fmt"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// decodeBlob extracts the ccm-internal id from a codex auth.json. On
// Unix the symlink target is a ccm-stored credential file with `id`
// at top level. On Windows the wrapper-copy uses `ccmSourceId`.
// Returns ("", nil) for foreign (non-ccm) files.
func decodeBlob(blob []byte) (id string, err error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		return "", fmt.Errorf("codex: parse blob: %w", err)
	}
	if raw, ok := m["id"]; ok {
		_ = json.Unmarshal(raw, &id)
	}
	if id == "" {
		if raw, ok := m["ccmSourceId"]; ok {
			_ = json.Unmarshal(raw, &id)
		}
	}
	if id == "" {
		return "", nil
	}
	if raw, ok := m["provider"]; ok {
		var prov string
		_ = json.Unmarshal(raw, &prov)
		if prov != "" && prov != "codex" {
			return "", nil
		}
	}
	return id, nil
}

// encodeBlob marshals a codex credential to its on-disk JSON shape.
func encodeBlob(cred *store.Credential) ([]byte, error) {
	if cred.ProviderName() != "codex" {
		return nil, fmt.Errorf("codex: encode non-codex credential")
	}
	return json.MarshalIndent(cred, "", "  ")
}
