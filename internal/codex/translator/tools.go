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
