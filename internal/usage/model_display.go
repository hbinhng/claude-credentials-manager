package usage

import "regexp"

var dateSuffixRE = regexp.MustCompile(`-\d{8}$`)

// NormalizeModelID strips a trailing -YYYYMMDD date suffix from an
// Anthropic model ID. Input without such a suffix is returned unchanged.
func NormalizeModelID(id string) string {
	return dateSuffixRE.ReplaceAllString(id, "")
}

var modelDisplayTable = map[string]string{
	"claude-opus-4-7":   "Opus 4.7",
	"claude-sonnet-4-6": "Sonnet 4.6",
	"claude-haiku-4-5":  "Haiku 4.5",
}

// ModelDisplay returns the short display name for an Anthropic model ID.
// Falls through to the date-stripped ID for unknown entries.
func ModelDisplay(id string) string {
	stripped := NormalizeModelID(id)
	if pretty, ok := modelDisplayTable[stripped]; ok {
		return pretty
	}
	return stripped
}
