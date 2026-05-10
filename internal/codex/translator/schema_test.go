package translator

import (
	"reflect"
	"testing"
	"unicode/utf8"
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
	// No sanitizer registered → early return; argsJSON passes through unchanged.
	in := `{"x":""}`
	got := sanitizeJSONStringForTool("UnknownTool", in)
	if got != in {
		t.Errorf("unknown tool should pass through, got %v", got)
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

func TestTruncateAtWordBoundary_PreservesUTF8AfterHardCut(t *testing.T) {
	// 5 ASCII bytes + 3-byte CJK rune = 8 bytes; max=7 lands inside the rune.
	in := "abcde" + "\xe7\x95\x8c"
	if utf8.RuneCountInString(in) != 6 {
		t.Fatalf("test setup wrong: rune count = %d", utf8.RuneCountInString(in))
	}
	got := truncateAtWordBoundary(in, 7)
	if !utf8.ValidString(got) {
		t.Errorf("truncated output is invalid UTF-8: %q", got)
	}
	// Expected behavior: back up to the rune boundary at byte 5.
	if got != "abcde" {
		t.Errorf("truncate(7) = %q, want \"abcde\"", got)
	}
}

func TestTruncateAtWordBoundary_LeadingWhitespaceOnlyDropsToEmpty(t *testing.T) {
	// String starts with whitespace at byte 0, no other whitespace.
	in := "\nabcdefghij"
	got := truncateAtWordBoundary(in, 5)
	// lastWhitespaceIndex returns 0; with the i>=0 guard, cut[:0] = "".
	if got != "" {
		t.Errorf("truncate(5) on leading-whitespace-only input = %q, want empty (whitespace boundary at index 0)", got)
	}
}
