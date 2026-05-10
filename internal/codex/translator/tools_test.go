package translator

import (
	"encoding/json"
	"testing"
)

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


