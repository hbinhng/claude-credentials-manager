// Package translator: tool surface alignment between Claude Code's
// builtin tools and codex's. The drop list strips Claude-Code-only
// tools the model has never been trained on. The rename map (added in
// Phase 4+) maps overlapping tools to their codex shape.
package translator

import "encoding/json"

// droppedClaudeTools enumerates Claude Code builtin tools that codex
// has no analog for. They are stripped from outbound `tools[]` so the
// model cannot call them — preventing silent-no-op loops like the
// TaskUpdate loop documented in the comprehensive-fix spec.
//
// The set is hardcoded by design (no env config). Maintainers should
// review on Claude Code releases that add or rename builtin tools.
//
// MCP tools (`mcp__*` prefix) are NOT in this set — they are
// user-configured and pass through unchanged.
//
// Overlapping tools (Bash, Read, Edit, Write, Glob, Grep, WebSearch,
// WebFetch) are NOT in this set — they get renamed to codex's
// equivalents in Phase 4+.
var droppedClaudeTools = map[string]struct{}{
	"TaskCreate": {}, "TaskList": {}, "TaskGet": {},
	"TaskUpdate": {}, "TaskStop": {}, "TaskOutput": {},
	"CronCreate": {}, "CronList": {}, "CronDelete": {},
	"Monitor":       {},
	"LSP":           {},
	"Skill":         {},
	"EnterWorktree": {}, "ExitWorktree": {},
	"RemoteTrigger":    {},
	"PushNotification": {},
	"NotebookEdit":     {},
	"ScheduleWakeup":   {},
	"EnterPlanMode":    {}, "ExitPlanMode": {},
	"Agent":           {},
	"AskUserQuestion": {},
	"TodoWrite":       {},
}

// isDroppedClaudeTool reports whether a tool name is in the drop list.
func isDroppedClaudeTool(name string) bool {
	_, ok := droppedClaudeTools[name]
	return ok
}

// toolRename describes a bidirectional name+arg-key mapping between a
// Claude Code tool and a codex tool. Forward path (Claude → codex):
// emit a tool definition with name=To and a hand-written schema; remap
// param keys via ParamRename when serializing assistant tool_use
// arguments. Reverse path (codex → Claude): rename function_call.name
// from To back to From; remap arg keys via ParamReverseRename.
type toolRename struct {
	From               string
	To                 string
	ParamRename        map[string]string // forward: Claude key → codex key
	ParamReverseRename map[string]string // reverse: codex key → Claude key
	OutputSchema       map[string]any    // codex's hand-written schema
}

// codexViewImageSchema mirrors codex-rs/core/src/tools/handlers/view_image_spec.rs.
var codexViewImageSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Filesystem path to the image or PDF.",
		},
	},
	"required":             []any{"path"},
	"additionalProperties": false,
}

// codexExecCommandSchema is the input schema for codex's exec_command
// tool. Mirrors the upstream codex-rs definition (shell.rs:91) but
// uses the keys codex's protocol actually expects on the wire.
var codexExecCommandSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"cmd": map[string]any{
			"type":        "string",
			"description": "The shell command to execute.",
		},
		"workdir": map[string]any{
			"type":        "string",
			"description": "Working directory for the command.",
		},
		"yield_time_ms": map[string]any{
			"type":        "integer",
			"description": "How long to let the command run before yielding.",
		},
	},
	"required":             []any{"cmd"},
	"additionalProperties": false,
}

// toolRenameMap is keyed by the Claude tool name (forward direction).
var toolRenameMap = map[string]toolRename{
	"Bash": {
		From:               "Bash",
		To:                 "exec_command",
		ParamRename:        map[string]string{"command": "cmd"},
		ParamReverseRename: map[string]string{"cmd": "command"},
		OutputSchema:       codexExecCommandSchema,
	},
	// Read is a special case: we EMIT BOTH `Read` (kept verbatim for
	// text reads) AND `view_image` (codex's image/PDF tool). The
	// forward path adds view_image as a synthetic extra entry; the
	// reverse path renames view_image → Read and `path` → `file_path`.
	"Read": {
		From:               "Read",
		To:                 "view_image",
		ParamRename:        nil, // forward keeps Read tool_use unchanged
		ParamReverseRename: map[string]string{"path": "file_path"},
		OutputSchema:       codexViewImageSchema,
	},
}

// reverseRenameLookup is built once at init time for O(1) reverse
// lookups by codex name.
var reverseRenameLookup = func() map[string]toolRename {
	m := make(map[string]toolRename, len(toolRenameMap))
	for _, r := range toolRenameMap {
		m[r.To] = r
	}
	return m
}()

func lookupForwardRename(claudeName string) (toolRename, bool) {
	r, ok := toolRenameMap[claudeName]
	return r, ok
}

func lookupReverseName(codexName string) (string, bool) {
	r, ok := reverseRenameLookup[codexName]
	if !ok {
		return "", false
	}
	return r.From, true
}

func lookupReverseRename(codexName string) (toolRename, bool) {
	r, ok := reverseRenameLookup[codexName]
	return r, ok
}

// applyForwardToolDef returns the codex tool entry for a Claude tool.
// If the tool has a rename mapping with a non-empty ParamRename map,
// the Claude tool is fully replaced with the codex name + hand-written
// schema (e.g. Bash → exec_command). If the rename has an empty
// ParamRename map (e.g. Read), the Claude tool is forwarded verbatim;
// the caller is responsible for also emitting the synthetic codex entry.
func applyForwardToolDef(in anthropicTool) codexTool {
	if r, ok := lookupForwardRename(in.Name); ok && len(r.ParamRename) > 0 {
		// Renames with a non-empty ParamRename map fully replace the
		// Claude tool (Bash → exec_command). Renames with an empty
		// ParamRename map are "additive" (Read keeps its entry, but
		// view_image is also emitted by the caller).
		return codexTool{
			Type:        "function",
			Name:        r.To,
			Description: defaultDescription(in.Description),
			Parameters:  r.OutputSchema,
		}
	}
	return codexTool{
		Type:        "function",
		Name:        in.Name,
		Description: defaultDescription(in.Description),
		Parameters:  defaultParameters(in.InputSchema),
	}
}

// reverseRenameArgs takes a codex tool name and a JSON-encoded
// arguments string, renames keys via ParamReverseRename, and returns
// the re-serialized JSON. Returns the input unchanged if the tool has
// no rename mapping or the JSON is malformed.
func reverseRenameArgs(codexName, argsJSON string) string {
	r, ok := lookupReverseRename(codexName)
	if !ok || len(r.ParamReverseRename) == 0 {
		return argsJSON
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return argsJSON
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if nk, hit := r.ParamReverseRename[k]; hit {
			out[nk] = v
		} else {
			out[k] = v
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Unreachable: out is a map[string]any populated from a successful
		// Unmarshal, so all values are JSON-representable. Defensive return.
		return argsJSON
	}
	return string(b)
}

// codexApplyPatchSchema mirrors codex-rs/core/src/tools/handlers/apply_patch_spec.rs.
var codexApplyPatchSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"patch": map[string]any{
			"type":        "string",
			"description": "Unified-diff patch describing the change.",
		},
		"filename": map[string]any{
			"type":        "string",
			"description": "Target filename.",
		},
	},
	"required":             []any{"patch", "filename"},
	"additionalProperties": false,
}

// editToolNames are the Claude tools Phase 6 collapses into apply_patch.
var editToolNames = map[string]struct{}{
	"Edit":  {},
	"Write": {},
}

func isEditOrWrite(name string) bool {
	_, ok := editToolNames[name]
	return ok
}

// editToolUseToApplyPatchArgs synthesizes apply_patch arguments from a
// Claude tool_use{Edit|Write, input}. Returns ("", false) on input
// shape mismatch — caller falls back to passthrough.
func editToolUseToApplyPatchArgs(toolName string, input any) (string, bool) {
	m, ok := input.(map[string]any)
	if !ok {
		return "", false
	}
	filename, _ := m["file_path"].(string)
	if filename == "" {
		return "", false
	}
	var diff string
	switch toolName {
	case "Edit":
		oldStr, _ := m["old_string"].(string)
		newStr, _ := m["new_string"].(string)
		if oldStr == "" && newStr == "" {
			return "", false
		}
		diff = synthesizeUnifiedDiff(filename, oldStr, newStr)
	case "Write":
		content, _ := m["content"].(string)
		diff = synthesizeFileCreateDiff(filename, content)
	default:
		return "", false
	}
	args, err := json.Marshal(map[string]any{"patch": diff, "filename": filename})
	if err != nil {
		// Unreachable: map[string]any with string values always marshals
		// successfully. Defensive guard retained for safety.
		return "", false
	}
	return string(args), true
}

// ApplyPatchReverse parses argsJSON (apply_patch arguments) and
// returns the Anthropic tool name + args map to surface to Claude
// Code. On parse failure or unsupported diff, returns ("", nil, false)
// — caller falls back to passthrough.
// Exported so the round-trip test in request_test.go can call it directly.
func ApplyPatchReverse(argsJSON string) (claudeName string, claudeArgs map[string]any, ok bool) {
	return applyPatchReverse(argsJSON)
}

// applyPatchReverse is the unexported implementation used by the stream translator.
func applyPatchReverse(argsJSON string) (claudeName string, claudeArgs map[string]any, ok bool) {
	var raw struct {
		Patch    string `json:"patch"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return "", nil, false
	}
	if raw.Patch == "" {
		return "", nil, false
	}
	parsed, err := parseUnifiedDiff(raw.Patch)
	if err != nil {
		return "", nil, false
	}
	switch parsed.kind {
	case diffKindCreate:
		return "Write", map[string]any{
			"file_path": parsed.filename,
			"content":   parsed.fileContent,
		}, true
	case diffKindEdit:
		if len(parsed.edits) == 0 {
			// Unreachable: parseUnifiedDiff returns an error (not diffKindEdit)
			// when there are no -/+ lines. Defensive guard retained for safety.
			return "", nil, false
		}
		// Multi-hunk: surface only the first edit. Future work may emit
		// multiple parallel tool_use blocks; v1 just picks the first.
		e := parsed.edits[0]
		return "Edit", map[string]any{
			"file_path":  parsed.filename,
			"old_string": e.oldString,
			"new_string": e.newString,
		}, true
	}
	// Unreachable: parseUnifiedDiff only returns diffKindUnknown=0 for the
	// zero value, which cannot occur because errors take the early return path
	// and all successful parses produce diffKindEdit or diffKindCreate.
	return "", nil, false
}

// applyForwardArgRename rewrites the keys of args via the forward
// rename map for the given Claude tool name. If the tool has no
// mapping, args is returned unchanged. If args is nil, returns nil.
func applyForwardArgRename(claudeName string, args any) any {
	r, ok := lookupForwardRename(claudeName)
	if !ok || len(r.ParamRename) == 0 {
		return args
	}
	m, ok := args.(map[string]any)
	if !ok {
		return args
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if nk, hit := r.ParamRename[k]; hit {
			out[nk] = v
		} else {
			out[k] = v
		}
	}
	return out
}
