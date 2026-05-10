// Package translator: tool surface alignment between Claude Code's
// builtin tools and codex's. The drop list strips Claude-Code-only
// tools the model has never been trained on. The rename map (added in
// Phase 4+) maps overlapping tools to their codex shape.
package translator

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
// Phase 4 ships only the Bash entry; later phases append.
var toolRenameMap = map[string]toolRename{
	"Bash": {
		From:               "Bash",
		To:                 "exec_command",
		ParamRename:        map[string]string{"command": "cmd"},
		ParamReverseRename: map[string]string{"cmd": "command"},
		OutputSchema:       codexExecCommandSchema,
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
