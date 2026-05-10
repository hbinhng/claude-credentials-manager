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
