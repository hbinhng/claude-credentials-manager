package shellalias

import (
	"strings"
	"testing"
)

func TestRcSnippet_POSIX(t *testing.T) {
	got := buildRcSnippet("posix", "/home/u/.ccm/aliases.sh")
	if !strings.Contains(got, "# ccm-aliases:begin") || !strings.Contains(got, "# ccm-aliases:end") {
		t.Fatalf("missing sentinels: %q", got)
	}
	if !strings.Contains(got, `[ -f "/home/u/.ccm/aliases.sh" ]`) {
		t.Fatalf("path not baked: %q", got)
	}
	if !strings.Contains(got, `. "/home/u/.ccm/aliases.sh"`) {
		t.Fatalf("missing source line: %q", got)
	}
}

func TestRcSnippet_Fish(t *testing.T) {
	got := buildRcSnippet("fish", "/home/u/.ccm/aliases.fish")
	if !strings.Contains(got, "source \"/home/u/.ccm/aliases.fish\"") {
		t.Fatalf("missing source line: %q", got)
	}
}

func TestRcSnippet_Pwsh(t *testing.T) {
	got := buildRcSnippet("pwsh", `C:\Users\u\.ccm\aliases.ps1`)
	if !strings.Contains(got, `. 'C:\Users\u\.ccm\aliases.ps1'`) {
		t.Fatalf("missing dot-source: %q", got)
	}
}

func TestRcSnippet_Pwsh_EscapesSingleQuoteInPath(t *testing.T) {
	got := buildRcSnippet("pwsh", `C:\Users\O'Brien\.ccm\aliases.ps1`)
	want := `'C:\Users\O''Brien\.ccm\aliases.ps1'`
	if !strings.Contains(got, want) {
		t.Fatalf("expected escaped path %q in snippet, got: %s", want, got)
	}
	// Ensure the line shape is still intact.
	if !strings.Contains(got, "if (Test-Path ") {
		t.Fatalf("missing Test-Path: %s", got)
	}
}

func TestRcSnippet_HasSentinel_Detects(t *testing.T) {
	if !hasRcSentinel([]byte("foo\n# ccm-aliases:begin\nbar\n# ccm-aliases:end\nbaz")) {
		t.Fatal("should detect")
	}
	if hasRcSentinel([]byte("just unrelated content")) {
		t.Fatal("false positive")
	}
}

func TestRcSnippet_EnsureAppendsIfMissing(t *testing.T) {
	got := ensureRcSnippet([]byte("existing rc content\n"), "posix", "/p")
	if !strings.Contains(string(got), "# ccm-aliases:begin") {
		t.Fatalf("not appended: %q", got)
	}
	if !strings.HasPrefix(string(got), "existing rc content\n") {
		t.Fatalf("clobbered existing: %q", got)
	}
}

func TestRcSnippet_EnsureSkipsIfPresent(t *testing.T) {
	in := []byte("# ccm-aliases:begin\nOLD\n# ccm-aliases:end\n")
	got := ensureRcSnippet(in, "posix", "/different/path")
	if string(got) != string(in) {
		t.Fatalf("should be no-op; got %q", got)
	}
}

func TestRcSnippet_EnsureCreatesIfEmpty(t *testing.T) {
	got := ensureRcSnippet(nil, "posix", "/p")
	if !strings.Contains(string(got), "# ccm-aliases:begin") {
		t.Fatalf("not created: %q", got)
	}
}

func TestRcSnippet_EnsureAppendsWithoutTrailingNewline(t *testing.T) {
	got := ensureRcSnippet([]byte("no newline at end"), "posix", "/p")
	if !strings.Contains(string(got), "no newline at end\n# ccm-aliases:begin") {
		t.Fatalf("missing inserted newline before sentinel: %q", got)
	}
}
