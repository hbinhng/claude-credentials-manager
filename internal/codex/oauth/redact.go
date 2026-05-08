package codexoauth

import "regexp"

var (
	rxJWT            = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	rxRotatingRT     = regexp.MustCompile(`rt_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	rxBearer         = regexp.MustCompile(`Bearer\s+[A-Za-z0-9_\-\.]+`)
	rxJSONTokenField = regexp.MustCompile(`("(?:access|refresh|id)_token"\s*:\s*)"[^"]+"`)
)

// Redact replaces every recognized token pattern in s with "<redacted>".
// Order matters: the JSON-field replacement runs first so it preserves
// the field name, then the freestanding-token patterns mop up anything
// that was outside JSON quoting.
func Redact(s string) string {
	s = rxJSONTokenField.ReplaceAllString(s, `${1}"<redacted>"`)
	s = rxJWT.ReplaceAllString(s, "<redacted>")
	s = rxRotatingRT.ReplaceAllString(s, "<redacted>")
	s = rxBearer.ReplaceAllString(s, "Bearer <redacted>")
	return s
}
