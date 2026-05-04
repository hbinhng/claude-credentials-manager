package claude

// Active returns the currently-active credential id and ok=true when
// ccm is managing the active backend entry. Reads through
// currentBackend().
func Active() (string, bool) {
	blob, ok, err := currentBackend().Read()
	if err != nil || !ok {
		return "", false
	}
	id, _, _, err := decodeBlob(blob)
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}

// IsActive reports whether the given credential id is the active one.
func IsActive(id string) bool {
	active, ok := Active()
	return ok && active == id
}

// ActiveID returns the active credential id, or "" when none is active.
func ActiveID() string {
	id, _ := Active()
	return id
}

// IsManaged reports whether ccm currently owns the Claude credential
// store entry (file or keychain).
func IsManaged() bool {
	_, ok := Active()
	return ok
}

// ReadActiveBlob exposes the raw active blob and whether one exists.
// Used by ccm backup to decide between sync and import-as-new without
// having to know which backend is in play.
func ReadActiveBlob() ([]byte, bool, error) {
	return currentBackend().Read()
}
