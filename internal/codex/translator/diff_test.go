package translator

import (
	"strings"
	"testing"
)

func TestSynthesizeUnifiedDiff_SingleHunk(t *testing.T) {
	got := synthesizeUnifiedDiff("foo.go", "old\n", "new\n")
	if !strings.Contains(got, "--- a/foo.go") {
		t.Errorf("missing --- header:\n%s", got)
	}
	if !strings.Contains(got, "+++ b/foo.go") {
		t.Errorf("missing +++ header:\n%s", got)
	}
	if !strings.Contains(got, "-old") {
		t.Errorf("missing - line:\n%s", got)
	}
	if !strings.Contains(got, "+new") {
		t.Errorf("missing + line:\n%s", got)
	}
}

func TestSynthesizeUnifiedDiff_FileCreate(t *testing.T) {
	got := synthesizeFileCreateDiff("new.go", "package main\n\nfunc main() {}\n")
	if !strings.Contains(got, "--- /dev/null") {
		t.Errorf("file-create diff should source from /dev/null:\n%s", got)
	}
	if !strings.Contains(got, "+++ b/new.go") {
		t.Errorf("missing +++ header:\n%s", got)
	}
	if !strings.Contains(got, "+package main") {
		t.Errorf("missing + lines for file content:\n%s", got)
	}
}

func TestParseUnifiedDiff_SingleEdit(t *testing.T) {
	diff := "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n"
	got, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if got.kind != diffKindEdit {
		t.Errorf("kind = %v, want diffKindEdit", got.kind)
	}
	if got.filename != "foo.go" {
		t.Errorf("filename = %q, want foo.go", got.filename)
	}
	if len(got.edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(got.edits))
	}
	if got.edits[0].oldString != "old\n" {
		t.Errorf("edit[0].oldString = %q, want \"old\\n\"", got.edits[0].oldString)
	}
	if got.edits[0].newString != "new\n" {
		t.Errorf("edit[0].newString = %q, want \"new\\n\"", got.edits[0].newString)
	}
}

func TestParseUnifiedDiff_FileCreate(t *testing.T) {
	diff := "--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+package main\n+\n"
	got, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if got.kind != diffKindCreate {
		t.Errorf("kind = %v, want diffKindCreate", got.kind)
	}
	if got.filename != "new.go" {
		t.Errorf("filename = %q, want new.go", got.filename)
	}
	if got.fileContent != "package main\n\n" {
		t.Errorf("fileContent = %q, want \"package main\\n\\n\"", got.fileContent)
	}
}

func TestParseUnifiedDiff_RenameUnsupported(t *testing.T) {
	diff := "--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-x\n+x\n"
	_, err := parseUnifiedDiff(diff)
	if err == nil {
		t.Errorf("rename diff (a/old.go → b/new.go with same content) should be unsupported")
	}
}

func TestParseUnifiedDiff_MissingHeaders(t *testing.T) {
	_, err := parseUnifiedDiff("@@ -1 +1 @@\n-old\n+new\n")
	if err == nil {
		t.Errorf("diff without --- / +++ headers should return error")
	}
}

func TestParseUnifiedDiff_NoMinusPlus(t *testing.T) {
	// A diff with headers and @@ hunk marker but no -/+ lines should error.
	diff := "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n"
	_, err := parseUnifiedDiff(diff)
	if err == nil {
		t.Errorf("diff with no -/+ lines should return error")
	}
}

func TestSynthesizeFileCreateDiff_NoTrailingNewline(t *testing.T) {
	// content without trailing newline — exercises the nLines++ branch.
	got := synthesizeFileCreateDiff("noeol.go", "package main")
	if !strings.Contains(got, "@@ -0,0 +1,1 @@") {
		t.Errorf("expected 1-line hunk header:\n%s", got)
	}
	if !strings.Contains(got, "+package main") {
		t.Errorf("expected +package main line:\n%s", got)
	}
}

func TestStripPrefixABDevNull_NoPrefix(t *testing.T) {
	// Path with neither a/ nor b/ prefix and not /dev/null — exercises the
	// final fallthrough return in stripPrefixABDevNull.
	diff := "--- foo.go\n+++ foo.go\n@@ -1 +1 @@\n-old\n+new\n"
	got, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if got.filename != "foo.go" {
		t.Errorf("filename = %q, want foo.go", got.filename)
	}
}
