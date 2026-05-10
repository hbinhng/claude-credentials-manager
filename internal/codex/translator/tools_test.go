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
