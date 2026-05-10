package translator

import (
	"testing"
)


func TestToolRename_NoMappingForGlob(t *testing.T) {
	if _, ok := lookupForwardRename("Glob"); ok {
		t.Errorf("Glob should have no rename mapping")
	}
	if _, ok := lookupReverseName("Glob"); ok {
		t.Errorf("Glob reverse lookup should miss")
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


