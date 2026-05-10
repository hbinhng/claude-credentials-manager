package translator

import (
	"errors"
	"fmt"
	"strings"
)

// diffKind classifies the parsed diff so the reverse translator can
// pick between Anthropic Edit and Anthropic Write.
type diffKind int

const (
	diffKindUnknown diffKind = iota
	diffKindEdit             // single-file replace; one or more (old,new) hunks
	diffKindCreate           // file create from /dev/null
)

type diffEdit struct {
	oldString string
	newString string
}

type parsedDiff struct {
	kind        diffKind
	filename    string
	edits       []diffEdit // populated when kind == diffKindEdit
	fileContent string     // populated when kind == diffKindCreate
}

// synthesizeUnifiedDiff produces a minimal unified diff that replaces
// oldStr with newStr in filename. The diff has zero context lines —
// it is sufficient for codex's apply_patch which interprets the patch
// against the actual file contents.
func synthesizeUnifiedDiff(filename, oldStr, newStr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", filename, filename)
	fmt.Fprintf(&b, "@@\n")
	for _, line := range strings.Split(strings.TrimRight(oldStr, "\n"), "\n") {
		fmt.Fprintf(&b, "-%s\n", line)
	}
	for _, line := range strings.Split(strings.TrimRight(newStr, "\n"), "\n") {
		fmt.Fprintf(&b, "+%s\n", line)
	}
	return b.String()
}

// synthesizeFileCreateDiff produces a unified diff that creates a new
// file with the given content.
func synthesizeFileCreateDiff(filename, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n", filename)
	nLines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") && content != "" {
		nLines++
	}
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", nLines)
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		fmt.Fprintf(&b, "+%s\n", line)
	}
	return b.String()
}

// parseUnifiedDiff parses a minimal unified diff and classifies it.
// Returns an error for renames, mode changes, binary diffs, or
// otherwise non-trivial multi-file diffs — the caller falls through
// to passthrough behavior.
func parseUnifiedDiff(diff string) (parsedDiff, error) {
	lines := strings.Split(diff, "\n")
	var srcPath, dstPath string
	body := []string{}
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- "):
			srcPath = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
		case strings.HasPrefix(line, "+++ "):
			dstPath = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			body = lines[i+1:]
		}
		if srcPath != "" && dstPath != "" {
			break
		}
	}
	if srcPath == "" || dstPath == "" {
		return parsedDiff{}, errors.New("diff missing --- / +++ headers")
	}

	src := stripPrefixABDevNull(srcPath)
	dst := stripPrefixABDevNull(dstPath)

	// file-create case
	if srcPath == "/dev/null" {
		var content strings.Builder
		for _, ln := range body {
			if strings.HasPrefix(ln, "@@") || ln == "" {
				continue
			}
			if strings.HasPrefix(ln, "+") {
				content.WriteString(strings.TrimPrefix(ln, "+"))
				content.WriteString("\n")
			}
		}
		return parsedDiff{
			kind:        diffKindCreate,
			filename:    dst,
			fileContent: content.String(),
		}, nil
	}

	// rename (different src and dst paths) is unsupported
	if src != dst {
		return parsedDiff{}, fmt.Errorf("rename diff (%s → %s) unsupported", src, dst)
	}

	// single or multi-hunk edit
	var edits []diffEdit
	var oldB, newB strings.Builder
	flush := func() {
		if oldB.Len() == 0 && newB.Len() == 0 {
			return
		}
		edits = append(edits, diffEdit{oldString: oldB.String(), newString: newB.String()})
		oldB.Reset()
		newB.Reset()
	}
	for _, ln := range body {
		switch {
		case strings.HasPrefix(ln, "@@"):
			flush()
		case strings.HasPrefix(ln, "-"):
			oldB.WriteString(strings.TrimPrefix(ln, "-"))
			oldB.WriteString("\n")
		case strings.HasPrefix(ln, "+"):
			newB.WriteString(strings.TrimPrefix(ln, "+"))
			newB.WriteString("\n")
		}
	}
	flush()
	if len(edits) == 0 {
		return parsedDiff{}, errors.New("diff has no -/+ lines")
	}
	return parsedDiff{kind: diffKindEdit, filename: dst, edits: edits}, nil
}

func stripPrefixABDevNull(p string) string {
	p = strings.TrimSpace(p)
	if p == "/dev/null" {
		return p
	}
	if strings.HasPrefix(p, "a/") {
		return strings.TrimPrefix(p, "a/")
	}
	if strings.HasPrefix(p, "b/") {
		return strings.TrimPrefix(p, "b/")
	}
	return p
}
