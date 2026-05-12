package share

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestCredPtrAudit enumerates every state.credPtr() call site in the
// share package's PRODUCTION files (not _test.go) and asserts each is
// one of:
//
//	(a) inside an !isPassthrough() branch (within ~6 lines above)
//	(b) followed by a nil-check on the result (within ~3 lines)
//	(c) explicitly annotated with "// passthrough-safe: <reason>"
//
// Spec §7 invariant: passthroughEntryState.credPtr() returns nil; any
// unguarded deref will NPE when a passthrough entry is the receiver.
//
// Updates to this audit: when adding a new credPtr() call site, either
// add one of the three guard forms or document an explicit waiver as
// "passthrough-safe: <reason>" in a nearby comment.
func TestCredPtrAudit(t *testing.T) {
	root := "."
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	re := regexp.MustCompile(`\.credPtr\(\)`)
	var sites []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(root, name)
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			// Skip the definition itself.
			if strings.Contains(line, "func ") && strings.Contains(line, "credPtr()") {
				continue
			}
			start := i - 6
			if start < 0 {
				start = 0
			}
			end := i + 3
			if end > len(lines) {
				end = len(lines)
			}
			window := strings.Join(lines[start:end], "\n")
			guarded := strings.Contains(window, "isPassthrough()") ||
				strings.Contains(window, "passthrough-safe:") ||
				containsNilCheck(lines, i)
			if !guarded {
				sites = append(sites, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(sites) > 0 {
		t.Errorf("unguarded credPtr() deref sites (must be !isPassthrough()-gated, nil-checked, or annotated with `passthrough-safe:`):\n  %s",
			strings.Join(sites, "\n  "))
	}
}

func containsNilCheck(lines []string, i int) bool {
	for j := i - 1; j >= i-3 && j >= 0; j-- {
		if strings.Contains(lines[j], "!= nil") || strings.Contains(lines[j], "== nil") {
			return true
		}
	}
	for j := i + 1; j < i+4 && j < len(lines); j++ {
		if strings.Contains(lines[j], "!= nil") || strings.Contains(lines[j], "== nil") {
			return true
		}
	}
	return false
}
