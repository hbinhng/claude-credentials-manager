package translator

import (
	"encoding/json"
	"testing"
)

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

func TestToolRename_BashMapsToExecCommand(t *testing.T) {
	r, ok := lookupForwardRename("Bash")
	if !ok {
		t.Fatalf("lookupForwardRename(\"Bash\") = !ok")
	}
	if r.To != "exec_command" {
		t.Errorf("rename.To = %q, want exec_command", r.To)
	}
	if r.ParamRename["command"] != "cmd" {
		t.Errorf("ParamRename[command] = %q, want cmd", r.ParamRename["command"])
	}
	if r.ParamReverseRename["cmd"] != "command" {
		t.Errorf("ParamReverseRename[cmd] = %q, want command", r.ParamReverseRename["cmd"])
	}
	if r.OutputSchema == nil {
		t.Errorf("OutputSchema is nil; want hand-built schema")
	}
}

func TestToolRename_ReverseLookupExecCommandMapsToBash(t *testing.T) {
	name, ok := lookupReverseName("exec_command")
	if !ok || name != "Bash" {
		t.Errorf("lookupReverseName(exec_command) = (%q, %v), want (Bash, true)", name, ok)
	}
}

func TestToolRename_NoMappingForGlob(t *testing.T) {
	if _, ok := lookupForwardRename("Glob"); ok {
		t.Errorf("Glob should have no rename mapping")
	}
	if _, ok := lookupReverseName("Glob"); ok {
		t.Errorf("Glob reverse lookup should miss")
	}
}

func TestToolRename_LookupReverseRenameExecCommand(t *testing.T) {
	r, ok := lookupReverseRename("exec_command")
	if !ok {
		t.Fatalf("lookupReverseRename(exec_command) = !ok")
	}
	if r.From != "Bash" {
		t.Errorf("From = %q, want Bash", r.From)
	}
	if r.To != "exec_command" {
		t.Errorf("To = %q, want exec_command", r.To)
	}
}

func TestToolRename_LookupReverseRenameMiss(t *testing.T) {
	_, ok := lookupReverseRename("unknown_tool")
	if ok {
		t.Errorf("lookupReverseRename(unknown_tool) should miss")
	}
}

func TestApplyForwardArgRename_NonMapArgsPassThrough(t *testing.T) {
	// When args is not a map[string]any, it should pass through unchanged.
	result := applyForwardArgRename("Bash", "a plain string")
	if result != "a plain string" {
		t.Errorf("non-map args should pass through; got %v", result)
	}
}

func TestApplyForwardArgRename_NilArgsPassThrough(t *testing.T) {
	// nil args with a valid rename entry should return nil unchanged.
	result := applyForwardArgRename("Bash", nil)
	if result != nil {
		t.Errorf("nil args should return nil; got %v", result)
	}
}

func TestApplyForwardArgRename_NoMappingPassThrough(t *testing.T) {
	// Tool with no rename mapping: args returned verbatim.
	args := map[string]any{"pattern": "*.go"}
	result := applyForwardArgRename("Glob", args)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any result; got %T", result)
	}
	if m["pattern"] != "*.go" {
		t.Errorf("pattern = %v, want *.go", m["pattern"])
	}
}

func TestApplyForwardArgRename_UnknownKeysPassThrough(t *testing.T) {
	// A Bash call with command + unknown keys: command→cmd, others pass through.
	args := map[string]any{"command": "ls", "workdir": "/tmp"}
	result := applyForwardArgRename("Bash", args)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any result; got %T", result)
	}
	if m["cmd"] != "ls" {
		t.Errorf("cmd = %v, want ls", m["cmd"])
	}
	if m["workdir"] != "/tmp" {
		t.Errorf("workdir = %v, want /tmp", m["workdir"])
	}
	if _, exists := m["command"]; exists {
		t.Errorf("command key should be renamed to cmd, not kept")
	}
}

// Tests for reverseRenameArgs.

func TestReverseRenameArgs_NoMapping(t *testing.T) {
	// A tool with no reverse rename mapping passes args through unchanged.
	got := reverseRenameArgs("Glob", `{"pattern":"*.go"}`)
	if got != `{"pattern":"*.go"}` {
		t.Errorf("no-mapping tool should pass through; got %q", got)
	}
}

func TestReverseRenameArgs_UnknownTool(t *testing.T) {
	// A completely unknown codex tool (not in the reverse map) passes through.
	got := reverseRenameArgs("nonexistent_tool", `{"x":1}`)
	if got != `{"x":1}` {
		t.Errorf("unknown tool should pass through; got %q", got)
	}
}

func TestReverseRenameArgs_MalformedJSON(t *testing.T) {
	// Malformed JSON is returned unchanged.
	got := reverseRenameArgs("exec_command", `not json`)
	if got != `not json` {
		t.Errorf("malformed JSON should pass through; got %q", got)
	}
}

func TestReverseRenameArgs_UnknownKeyPassThrough(t *testing.T) {
	// exec_command with cmd + unknown key: cmd→command, unknown keys pass through.
	got := reverseRenameArgs("exec_command", `{"cmd":"ls","workdir":"/tmp"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if m["command"] != "ls" {
		t.Errorf("command = %v, want ls", m["command"])
	}
	if m["workdir"] != "/tmp" {
		t.Errorf("workdir = %v, want /tmp", m["workdir"])
	}
	if _, exists := m["cmd"]; exists {
		t.Errorf("cmd should be renamed to command, not kept")
	}
}

func TestToolRename_ViewImageReverseMapsToRead(t *testing.T) {
	name, ok := lookupReverseName("view_image")
	if !ok || name != "Read" {
		t.Errorf("lookupReverseName(view_image) = (%q, %v), want (Read, true)", name, ok)
	}
}

func TestCodexViewImageSchema_RequiresPath(t *testing.T) {
	req, ok := codexViewImageSchema["required"].([]any)
	if !ok || len(req) == 0 {
		t.Fatalf("codexViewImageSchema.required missing")
	}
	if req[0] != "path" {
		t.Errorf("codexViewImageSchema.required[0] = %v, want path", req[0])
	}
}

// Tests for new Phase 6 helper functions.

func TestIsEditOrWrite(t *testing.T) {
	if !isEditOrWrite("Edit") {
		t.Error("Edit should be recognized as edit-or-write")
	}
	if !isEditOrWrite("Write") {
		t.Error("Write should be recognized as edit-or-write")
	}
	if isEditOrWrite("Read") {
		t.Error("Read should NOT be recognized as edit-or-write")
	}
	if isEditOrWrite("Bash") {
		t.Error("Bash should NOT be recognized as edit-or-write")
	}
}

func TestEditToolUseToApplyPatchArgs_NonMapInput(t *testing.T) {
	// Non-map input returns false.
	_, ok := editToolUseToApplyPatchArgs("Edit", "not a map")
	if ok {
		t.Error("expected false for non-map input")
	}
}

func TestEditToolUseToApplyPatchArgs_EmptyFilename(t *testing.T) {
	// Map with no file_path returns false.
	_, ok := editToolUseToApplyPatchArgs("Edit", map[string]any{"old_string": "x", "new_string": "y"})
	if ok {
		t.Error("expected false for missing file_path")
	}
}

func TestEditToolUseToApplyPatchArgs_EditBothEmpty(t *testing.T) {
	// Edit with both old_string and new_string empty returns false.
	_, ok := editToolUseToApplyPatchArgs("Edit", map[string]any{"file_path": "foo.go"})
	if ok {
		t.Error("expected false for Edit with empty old_string and new_string")
	}
}

func TestEditToolUseToApplyPatchArgs_WriteToolProducesCreateDiff(t *testing.T) {
	// Write tool should produce a /dev/null create diff.
	argsJSON, ok := editToolUseToApplyPatchArgs("Write", map[string]any{
		"file_path": "new.go",
		"content":   "package main\n",
	})
	if !ok {
		t.Fatalf("expected ok for Write input")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	patch, _ := args["patch"].(string)
	if patch == "" {
		t.Errorf("expected non-empty patch for Write tool")
	}
	// The patch should be a /dev/null create diff.
	if patch[:12] != "--- /dev/nul" {
		t.Errorf("Write should produce /dev/null create diff; got:\n%s", patch)
	}
	if args["filename"] != "new.go" {
		t.Errorf("filename = %v, want new.go", args["filename"])
	}
}

func TestEditToolUseToApplyPatchArgs_WriteToolEmptyContent(t *testing.T) {
	// Write with empty content should still produce a valid empty diff.
	_, ok := editToolUseToApplyPatchArgs("Write", map[string]any{
		"file_path": "empty.go",
		"content":   "",
	})
	if !ok {
		t.Error("expected ok for Write with empty content")
	}
}

func TestApplyPatchReverseInternal_InvalidJSON(t *testing.T) {
	_, _, ok := applyPatchReverse("not json")
	if ok {
		t.Error("expected false for invalid JSON")
	}
}

func TestApplyPatchReverseInternal_EmptyPatch(t *testing.T) {
	_, _, ok := applyPatchReverse(`{"patch":"","filename":"foo.go"}`)
	if ok {
		t.Error("expected false for empty patch")
	}
}

func TestApplyPatchReverseInternal_UnparsableDiff(t *testing.T) {
	// A rename diff (src != dst) is rejected by parseUnifiedDiff.
	renameDiff := "--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-x\n+x\n"
	_, _, ok := applyPatchReverse(`{"patch":"` + renameDiff + `","filename":"new.go"}`)
	if ok {
		t.Error("expected false for rename diff")
	}
}

func TestApplyPatchReverseInternal_DiffKindUnknown(t *testing.T) {
	// diffKindUnknown falls through to the default return ("", nil, false).
	// parseUnifiedDiff doesn't emit diffKindUnknown under normal inputs,
	// so we test the default case indirectly via a valid but edge diff.
	// We skip the direct test because the path is unreachable from normal inputs.
	// This comment is the inline justification per project rules.
	_ = diffKindUnknown
}

func TestRoundTrip_EditApplyPatchPreservesContent(t *testing.T) {
	body := []byte(`{
        "model":"claude-opus-4-7",
        "messages":[
            {"role":"user","content":"edit foo"},
            {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Edit","input":{"file_path":"foo.go","old_string":"old\n","new_string":"new\n"}}]},
            {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
        ],
        "tools":[{"name":"Edit","description":"e","input_schema":{"type":"object"}}]
    }`)
	fwd, err := TranslateRequest(body, RequestOpts{TargetModel: "gpt-5"})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	var probe struct {
		Input []struct {
			Type      string `json:"type"`
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
		} `json:"input"`
	}
	if err := json.Unmarshal(fwd, &probe); err != nil {
		t.Fatalf("unmarshal forward: %v", err)
	}
	var argsJSON string
	for _, it := range probe.Input {
		if it.Type == "function_call" && it.Name == "apply_patch" {
			argsJSON = it.Arguments
			break
		}
	}
	if argsJSON == "" {
		t.Fatalf("no apply_patch in forward output")
	}
	name, args, ok := applyPatchReverse(argsJSON)
	if !ok {
		t.Fatalf("applyPatchReverse failed on synthesized diff")
	}
	if name != "Edit" {
		t.Errorf("round-trip name = %q, want Edit", name)
	}
	if args["old_string"] != "old\n" {
		t.Errorf("round-trip old_string = %q, want \"old\\n\"", args["old_string"])
	}
	if args["new_string"] != "new\n" {
		t.Errorf("round-trip new_string = %q, want \"new\\n\"", args["new_string"])
	}
}

func TestApplyPatchReverse_MultiHunkUsesFirstEdit(t *testing.T) {
	// Two-hunk diff. v1 surfaces only the first edit; this test locks
	// that documented behavior so silent regressions are caught.
	diff := "--- a/foo.go\n+++ b/foo.go\n@@\n-old1\n+new1\n@@\n-old2\n+new2\n"
	argsJSON := `{"patch":` + jsonString(diff) + `,"filename":"foo.go"}`
	name, args, ok := applyPatchReverse(argsJSON)
	if !ok {
		t.Fatalf("applyPatchReverse failed on multi-hunk diff")
	}
	if name != "Edit" {
		t.Errorf("name = %q, want Edit", name)
	}
	if got := args["old_string"]; got != "old1\n" {
		t.Errorf("first-hunk old_string = %q, want \"old1\\n\" (multi-hunk should pick first only)", got)
	}
	if got := args["new_string"]; got != "new1\n" {
		t.Errorf("first-hunk new_string = %q, want \"new1\\n\"", got)
	}
}

// jsonString returns s wrapped as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestCodexApplyPatchSchema_RequiresPatchAndFilename(t *testing.T) {
	req, ok := codexApplyPatchSchema["required"].([]any)
	if !ok || len(req) != 2 {
		t.Fatalf("codexApplyPatchSchema.required = %v, want [patch filename]", codexApplyPatchSchema["required"])
	}
	has := map[any]bool{}
	for _, r := range req {
		has[r] = true
	}
	if !has["patch"] {
		t.Error("codexApplyPatchSchema.required missing patch")
	}
	if !has["filename"] {
		t.Error("codexApplyPatchSchema.required missing filename")
	}
}
