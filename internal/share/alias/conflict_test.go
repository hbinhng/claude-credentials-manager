package alias

import "testing"

// TestSplitOnStar_AdjacentStars verifies that consecutive stars are
// collapsed so "a**b" behaves identically to "a*b".
func TestSplitOnStar_AdjacentStars(t *testing.T) {
	got := splitOnStar("a**b")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitOnStar(\"a**b\") = %v, want [a b]", got)
	}
	// Also verify matching behaviour is identical to single star.
	ra, _ := compilePattern("a**b")
	rb, _ := compilePattern("a*b")
	for _, s := range []string{"ab", "aXb", "aXXb", ""} {
		if ra.matches(s) != rb.matches(s) {
			t.Errorf("a**b.matches(%q) = %v, a*b.matches(%q) = %v; should agree",
				s, ra.matches(s), s, rb.matches(s))
		}
	}
}

// TestAcceptsState_NonEmptyLaterSegment exercises the return-false branch
// of acceptsState: phase is within bounds, pos == len(segment), but a
// later segment is non-empty.
func TestAcceptsState_NonEmptyLaterSegment(t *testing.T) {
	// Pattern "a*b" → segments ["a", "b"]. At (phase=0, pos=1) we've
	// consumed "a" but we're still before the star (actually at end of
	// segment 0). There is a later non-empty segment "b", so this should
	// not accept.
	r, _ := compilePattern("a*b")
	if r.acceptsState(0, 1) {
		t.Error("acceptsState(0, 1) on \"a*b\" should be false: later segment \"b\" is non-empty")
	}
	// Sanity: the full match of "ab" should still succeed.
	if !r.matches("ab") {
		t.Error("a*b should match \"ab\"")
	}
}

// TestDefensiveGuards directly exercises the unreachable defensive guards in
// transitions and acceptsState to ensure they behave correctly if ever
// reached by future callers. The guards are unreachable from the normal
// matches/patternsConflict paths (confirmed by code review).
func TestDefensiveGuards(t *testing.T) {
	r, _ := compilePattern("abc") // segments = ["abc"]

	// transitions with phase beyond last segment should return nil.
	got := r.transitions(1, 0, 'a') // phase=1 == len(segments)
	if got != nil {
		t.Errorf("transitions(phase=len, ...) = %v, want nil", got)
	}

	// acceptsState with phase beyond last segment should return true.
	if !r.acceptsState(1, 0) { // phase=1 == len(segments)
		t.Error("acceptsState(phase=len, 0) should return true")
	}
}

func TestPatternsConflict(t *testing.T) {
	cases := []struct {
		name     string
		a, b     string
		conflict bool
	}{
		{"identical literals", "claude-opus-4.7", "claude-opus-4.7", true},
		{"different literals", "claude-opus", "claude-haiku", false},
		{"prefix-overlap", "claude-*", "claude-opus-*", true},
		{"disjoint prefixes", "claude-*", "gpt-*", false},
		{"universal vs specific literal", "*", "claude-opus", true},
		{"universal vs anything literal", "*", "anything", true}, // adversarial: prefix == suffix
		{"universal vs empty", "*", "", true},                    // empty matches "*"
		{"empty vs empty", "", "", true},
		{"empty vs literal", "", "claude", false},
		{"middle vs prefix", "claude-*-opus", "claude-haiku-*", true}, // witness: claude-haiku-opus
		{"middle vs prefix non-overlap", "claude-*-opus", "claude-haiku", false},
		{"two suffixes overlap", "*-codex", "*-mini", false},
		{"two suffixes same", "*-codex", "*-codex", true},
		{"two prefixes", "claude-*", "claudette-*", false},
		{"prefix subsumes literal", "claude-*", "claude-opus-4.7", true},
		{"literal not in prefix", "gpt-4", "claude-*", false},
		{"chained stars", "a*b*c", "a*c", true}, // both accept "abc"
		{"chained vs disjoint", "a*b", "c*d", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ra, _ := compilePattern(c.a)
			rb, _ := compilePattern(c.b)
			got := patternsConflict(ra, rb)
			if got != c.conflict {
				t.Errorf("patternsConflict(%q, %q) = %v, want %v", c.a, c.b, got, c.conflict)
			}
		})
	}
}
