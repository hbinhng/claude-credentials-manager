package codex

// Active returns the active credential ID and whether it is managed by ccm.
func Active() (string, bool) {
	blob, ok, err := currentBackend().Read()
	if err != nil || !ok {
		return "", false
	}
	id, _ := decodeBlob(blob)
	if id == "" {
		return "", false
	}
	return id, true
}

// ActiveID returns the active credential ID, or "" if none is active.
func ActiveID() string { id, _ := Active(); return id }

// IsActive reports whether cred with the given id is currently active.
func IsActive(id string) bool {
	a, ok := Active()
	return ok && a == id
}

// IsManaged reports whether ~/.codex/auth.json is currently managed by ccm.
func IsManaged() bool {
	_, ok := Active()
	return ok
}

// ReadActiveBlob returns the raw bytes of the active auth.json.
func ReadActiveBlob() ([]byte, bool, error) { return currentBackend().Read() }
