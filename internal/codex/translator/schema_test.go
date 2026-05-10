package translator

import (
	"reflect"
	"testing"
)

func TestSanitizeToolArguments_DropsEmptyPagesOnRead(t *testing.T) {
	in := map[string]any{"file_path": "foo.go", "pages": ""}
	got := sanitizeToolArguments("Read", in)
	want := map[string]any{"file_path": "foo.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSanitizeToolArguments_PreservesNonEmptyPages(t *testing.T) {
	in := map[string]any{"file_path": "foo.go", "pages": "1-5"}
	got := sanitizeToolArguments("Read", in)
	if got["pages"] != "1-5" {
		t.Errorf("pages = %v, want 1-5", got["pages"])
	}
}

func TestSanitizeToolArguments_UnknownToolPassthrough(t *testing.T) {
	in := map[string]any{"x": ""}
	got := sanitizeToolArguments("UnknownTool", in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("unknown tool args should pass through unchanged")
	}
}

func TestSanitizeToolArguments_NilArgsReturnsNil(t *testing.T) {
	got := sanitizeToolArguments("Read", nil)
	if got != nil {
		t.Errorf("nil args should return nil, got %v", got)
	}
}

func TestSanitizeJSONStringForTool_MalformedJSONPassthrough(t *testing.T) {
	// Malformed JSON should be returned unchanged.
	in := `not valid json`
	got := sanitizeJSONStringForTool("Read", in)
	if got != in {
		t.Errorf("malformed JSON should pass through, got %v", got)
	}
}

func TestSanitizeJSONStringForTool_UnknownToolPassthrough(t *testing.T) {
	// No sanitizer registered → passthrough without decoding.
	in := `{"x":""}`
	got := sanitizeJSONStringForTool("UnknownTool", in)
	if got != in {
		t.Errorf("unknown tool should pass through, got %v", got)
	}
}

func TestStripNullArgs_DropsNullKey(t *testing.T) {
	in := map[string]any{"keep": "x", "drop": nil}
	got := stripNullArgs(in).(map[string]any)
	if _, ok := got["drop"]; ok {
		t.Errorf("drop key should be removed")
	}
	if got["keep"] != "x" {
		t.Errorf("keep key should survive")
	}
}

func TestStripNullArgs_PreservesEmptyString(t *testing.T) {
	in := map[string]any{"x": ""}
	got := stripNullArgs(in).(map[string]any)
	if _, ok := got["x"]; !ok {
		t.Errorf("empty string is not null; should survive")
	}
}

func TestStripNullArgs_NonMapPassthrough(t *testing.T) {
	// Non-map values pass through unchanged (identity return).
	in := "just a string"
	got := stripNullArgs(in)
	if got != in {
		t.Errorf("non-map arg should pass through unchanged, got %v", got)
	}
}

func TestTruncateAtWordBoundary_Short(t *testing.T) {
	if got := truncateAtWordBoundary("hello", 100); got != "hello" {
		t.Errorf("short input should pass through, got %q", got)
	}
}

func TestTruncateAtWordBoundary_CutAtLastSpace(t *testing.T) {
	in := "hello there friend"
	got := truncateAtWordBoundary(in, 12)
	if got != "hello there" {
		t.Errorf("truncate(12) = %q, want \"hello there\"", got)
	}
}

func TestTruncateAtWordBoundary_NoSpaceFallsBackToHardCut(t *testing.T) {
	in := "abcdefghij"
	got := truncateAtWordBoundary(in, 5)
	if got != "abcde" {
		t.Errorf("no-space input truncate(5) = %q, want \"abcde\"", got)
	}
}
