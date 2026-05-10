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
