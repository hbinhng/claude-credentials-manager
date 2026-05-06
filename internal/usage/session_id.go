package usage

import "regexp"

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsValidSessionID reports whether s is a canonical UUID, case-insensitive.
// Anything else (empty, traversal sequences, separators, null bytes, non-hex)
// is rejected. This is the gate before any filesystem access keyed on s.
func IsValidSessionID(s string) bool {
	return uuidRE.MatchString(s)
}
