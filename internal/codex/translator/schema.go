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
// the tool's sanitizer, and re-encodes. If decoding or encoding
// fails, returns argsJSON unchanged. Used by the stream translator
// where args travel as opaque JSON strings.
func sanitizeJSONStringForTool(toolName, argsJSON string) string {
	if _, ok := argSanitizers[toolName]; !ok {
		return argsJSON
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return argsJSON
	}
	raw = sanitizeToolArguments(toolName, raw)
	out, err := json.Marshal(raw)
	if err != nil {
		// Unreachable: raw is a map[string]any populated from a successful
		// Unmarshal, so all values are JSON-representable. Defensive return.
		return argsJSON
	}
	return string(out)
}
