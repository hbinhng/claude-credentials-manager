package alias

// regex is a tiny NFA over the allowed alphabet. A glob pattern compiles
// into a list of literal segments separated by stars: "claude-*-opus"
// becomes ["claude-", "-opus"]. Free-loop transitions on the alphabet
// model the * between segments.
//
// Naming: this type is called `regex` for brevity but is really a
// segment-based NFA, not a general regex.
type regex struct {
	// segments holds the literal pieces between * boundaries.
	//   "claude-*-opus" → ["claude-", "-opus"]
	//   "claude-*"      → ["claude-", ""]
	//   "*-opus"        → ["", "-opus"]
	//   "*"             → ["", ""]
	//   "claude"        → ["claude"]
	//   ""              → [""]
	segments []string
}

// compilePattern parses a glob pattern into segments separated by *.
func compilePattern(pat string) (*regex, error) {
	return &regex{segments: splitOnStar(pat)}, nil
}

// splitOnStar splits "claude-*-opus" into ["claude-", "-opus"]. Adjacent
// stars are collapsed (so "**" behaves like "*"). Use a fresh nil slice
// per segment so subsequent appends don't alias the previous slice's
// backing array.
func splitOnStar(s string) []string {
	var parts []string
	var cur []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '*' {
			parts = append(parts, string(cur))
			cur = nil
			for i+1 < len(s) && s[i+1] == '*' {
				i++
			}
		} else {
			cur = append(cur, s[i])
		}
	}
	parts = append(parts, string(cur))
	return parts
}

// matches reports whether s matches the pattern. Anchored full-string
// match. Used by alias.Map.Lookup to find the matching rule for an
// inbound model name.
func (r *regex) matches(s string) bool {
	// State (phase, pos): we've consumed segments[0..phase-1] fully and
	// pos chars into segments[phase] (if phase < len). phase ==
	// len(segments) is accept.
	type state struct{ phase, pos int }
	cur := map[state]bool{{0, 0}: true}
	for i := 0; i < len(s); i++ {
		next := map[state]bool{}
		for st := range cur {
			for _, ns := range r.transitions(st.phase, st.pos, s[i]) {
				next[ns] = true
			}
		}
		cur = next
		if len(cur) == 0 {
			return false
		}
	}
	for st := range cur {
		if r.acceptsState(st.phase, st.pos) {
			return true
		}
	}
	return false
}

// transitions returns the set of next states reachable from (phase, pos)
// after consuming input character c. Each state is (phase, pos).
//
// Mid-segment (pos < len(segments[phase])): must match the next char of
// the current segment.
//
// End-of-segment (pos == len(segments[phase])): we're in the "free
// zone" of the * after this segment. Two options:
//
//	(a) Stay in the free zone: consume c, remain at (phase, pos).
//	(b) Try to start segments[phase+1]: if its first char == c, advance
//	    to (phase+1, 1). If segments[phase+1] is empty (e.g. trailing
//	    *), advance to (phase+1, 0) without further constraint AND
//	    still consume c by also looping in the free zone of (phase+1).
//
// Free zone exists only if phase+1 < len(segments). If we're at the end
// of segments[len-1] there's no trailing free zone unless the pattern
// ended with a *.
type transState = struct{ phase, pos int }

func (r *regex) transitions(phase, pos int, c byte) []transState {
	if phase >= len(r.segments) {
		// Unreachable from matches/patternsConflict: transitions only produces
		// states with phase < len(segments). Defensive guard kept for safety.
		return nil // already past last segment; no more input allowed
	}
	seg := r.segments[phase]
	if pos < len(seg) {
		// Mid-segment: must match.
		if seg[pos] == c {
			return []transState{{phase, pos + 1}}
		}
		return nil
	}
	// End-of-segment. Free zone valid only if there's a next segment.
	if phase+1 >= len(r.segments) {
		return nil // no trailing free zone; reject this input
	}
	out := []transState{{phase, pos}} // (a) stay
	next := r.segments[phase+1]
	if len(next) == 0 {
		// Empty next segment: enter its free zone immediately AND consume c.
		out = append(out, r.transitions(phase+1, 0, c)...)
	} else if next[0] == c {
		out = append(out, transState{phase + 1, 1})
	}
	return out
}

// acceptsState reports whether (phase, pos) is an accept state.
// Accept conditions:
//  1. phase == len(segments) — past the last segment.
//  2. phase < len(segments) AND pos == len(segments[phase]) AND every
//     segment after phase is empty — the suffix is ".*" or similar
//     and we've matched the required content.
func (r *regex) acceptsState(phase, pos int) bool {
	if phase == len(r.segments) {
		// Unreachable from matches/patternsConflict: transitions only produces
		// states with phase < len(segments); this arm is a defensive guard.
		return true
	}
	if phase < len(r.segments) && pos == len(r.segments[phase]) {
		for i := phase + 1; i < len(r.segments); i++ {
			if r.segments[i] != "" {
				return false
			}
		}
		return true
	}
	return false
}

// alphabet is the legal set of characters in inbound model names.
const conflictAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

// patternsConflict reports whether two patterns have a non-empty
// intersection: ∃ s such that A.matches(s) ∧ B.matches(s).
//
// Algorithm: BFS over the product NFA. Product states are (a_phase,
// a_pos, b_phase, b_pos). We start from (0, 0, 0, 0). For each input
// character in the alphabet, expand both NFAs' transitions and add the
// cartesian product to the frontier. Conflict iff we reach a product
// state where both component states accept.
//
// Bounded: the product state space is |A| * |B| where |A| = sum of
// segment lengths + 1 per phase. For practical inputs (≤10 patterns,
// ≤50 chars each), this is microseconds.
func patternsConflict(a, b *regex) bool {
	type pState struct{ aPhase, aPos, bPhase, bPos int }

	visited := map[pState]bool{}
	start := pState{0, 0, 0, 0}

	if a.acceptsState(start.aPhase, start.aPos) && b.acceptsState(start.bPhase, start.bPos) {
		return true
	}

	queue := []pState{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true

		for i := 0; i < len(conflictAlphabet); i++ {
			c := conflictAlphabet[i]
			nextA := a.transitions(cur.aPhase, cur.aPos, c)
			if len(nextA) == 0 {
				continue
			}
			nextB := b.transitions(cur.bPhase, cur.bPos, c)
			if len(nextB) == 0 {
				continue
			}
			for _, na := range nextA {
				for _, nb := range nextB {
					if a.acceptsState(na.phase, na.pos) && b.acceptsState(nb.phase, nb.pos) {
						return true
					}
					ps := pState{na.phase, na.pos, nb.phase, nb.pos}
					if !visited[ps] {
						queue = append(queue, ps)
					}
				}
			}
		}
	}
	return false
}
