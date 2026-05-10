package translator

import "encoding/json"

// argSanitizer is a per-tool helper that mutates the model's tool
// arguments on the reverse path before they reach Claude Code.
// Currently used to drop optional fields whose empty-string value
// would trigger Claude Code's input validators (e.g., Read.pages).
type argSanitizer func(map[string]any) map[string]any

// argSanitizers indexes per-tool sanitization rules.
var argSanitizers = map[string]argSanitizer{
	"Read": sanitizeReadArgs,
}

func sanitizeReadArgs(args map[string]any) map[string]any {
	if v, ok := args["pages"]; ok {
		if s, _ := v.(string); s == "" {
			delete(args, "pages")
		}
	}
	return args
}

// sanitizeToolArguments applies the named tool's sanitizer if any. If
// no sanitizer is registered, returns args unchanged.
func sanitizeToolArguments(toolName string, args map[string]any) map[string]any {
	fn, ok := argSanitizers[toolName]
	if !ok {
		return args
	}
	if args == nil {
		return nil
	}
	return fn(args)
}

// sanitizeJSONStringForTool decodes argsJSON as a JSON object, applies
// the tool's sanitizer (if registered) and always strips null-valued
// keys via stripNullArgs, then re-encodes. If decoding or encoding
// fails, returns argsJSON unchanged. Used by the stream translator
// where args travel as opaque JSON strings.
func sanitizeJSONStringForTool(toolName, argsJSON string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return argsJSON
	}
	if _, ok := argSanitizers[toolName]; ok {
		raw = sanitizeToolArguments(toolName, raw)
	}
	cleaned, ok := stripNullArgs(raw).(map[string]any)
	if !ok {
		// Unreachable: raw is map[string]any, so stripNullArgs always
		// returns map[string]any. Defensive fallback.
		cleaned = raw
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		// Unreachable: cleaned is map[string]any populated from a
		// successful Unmarshal, so all values are JSON-representable.
		return argsJSON
	}
	return string(out)
}

// toolResultMaxBytes caps the size of a forwarded tool_result string.
// Set well below the model's per-turn token budget; the truncator
// breaks at the last whitespace at or before the cap so a partial
// word like the observed "Updated task #1 ___" is avoided.
const toolResultMaxBytes = 64 * 1024

// truncateAtWordBoundary returns s if len(s) <= max, otherwise the
// longest prefix of length <= max that ends at a whitespace boundary.
// If no whitespace exists in the prefix, falls back to a hard cut at
// max. Callers (currently stringifyToolResult) use this to keep
// over-long tool_result strings from blowing chatgpt.com's input
// limit while preserving readability.
func truncateAtWordBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := lastWhitespaceIndex(cut); i > 0 {
		return cut[:i]
	}
	return cut
}

func lastWhitespaceIndex(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ' ', '\t', '\n':
			return i
		}
	}
	return -1
}
