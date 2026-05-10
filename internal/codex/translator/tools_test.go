package translator

import "testing"

func TestDroppedClaudeTools_ContainsLoopDrivers(t *testing.T) {
	mustHave := []string{
		"TaskCreate", "TaskList", "TaskGet", "TaskUpdate", "TaskStop", "TaskOutput",
		"CronCreate", "CronList", "CronDelete",
		"Monitor", "LSP", "Skill",
		"EnterWorktree", "ExitWorktree",
		"RemoteTrigger", "PushNotification",
		"NotebookEdit", "ScheduleWakeup",
		"EnterPlanMode", "ExitPlanMode",
		"Agent", "AskUserQuestion", "TodoWrite",
	}
	for _, name := range mustHave {
		if _, ok := droppedClaudeTools[name]; !ok {
			t.Errorf("droppedClaudeTools missing %q", name)
		}
	}
}

func TestDroppedClaudeTools_DoesNotContainOverlappingCodexTools(t *testing.T) {
	mustNotHave := []string{
		"Bash", "Read", "Edit", "Write", "Glob", "Grep", "WebSearch", "WebFetch",
	}
	for _, name := range mustNotHave {
		if _, ok := droppedClaudeTools[name]; ok {
			t.Errorf("droppedClaudeTools should NOT contain %q (kept for rename phases)", name)
		}
	}
}

func TestIsDroppedClaudeTool(t *testing.T) {
	// In the drop list — must return true.
	if !isDroppedClaudeTool("TaskUpdate") {
		t.Error("expected TaskUpdate to be dropped")
	}
	// Similar prefix but NOT in the list — must return false.
	if isDroppedClaudeTool("TaskFoo") {
		t.Error("expected TaskFoo NOT to be dropped")
	}
	// Overlapping codex tool (kept for rename phases) — must return false.
	if isDroppedClaudeTool("Bash") {
		t.Error("expected Bash NOT to be dropped")
	}
	// MCP tool (pass-through, never in the drop list) — must return false.
	if isDroppedClaudeTool("mcp__plugin__foo") {
		t.Error("expected mcp__plugin__foo NOT to be dropped")
	}
}
