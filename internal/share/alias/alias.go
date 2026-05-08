// Package alias implements --model-alias parsing and lookup for the
// share/launch/serve proxy. Alias rules rewrite the inbound `model`
// field on each request before pool selection.
//
// Pattern semantics (per spec §8.2):
//   - Source alphabet: [A-Za-z0-9._-]
//   - * desugars to (alphabet)*
//   - All other characters match literally; full-string anchored
//
// Conflict detection (boot-time): any pair of source patterns whose
// match-sets overlap is rejected. See conflict.go.
package alias

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by Parse. Use errors.Is for matching.
var (
	ErrSyntax      = errors.New("alias: syntax error")
	ErrInvalidGlob = errors.New("alias: invalid glob character")
	ErrConflict    = errors.New("alias: source patterns overlap")
)

// allowedAlphabet is the set of characters legal in alias patterns and
// model names. * is special; everything else here is matched literally.
const allowedAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

// Map is an ordered list of alias rules. Constructed via Parse;
// immutable after that.
type Map struct {
	rules []rule
}

type rule struct {
	source string // original pattern (for error messages)
	target string
	regex  *regex // compiled NFA representation of the source pattern
}

// Parse accepts repeated --model-alias values like "claude-opus-*=gpt-5"
// and returns an immutable Map. Errors:
//   - ErrSyntax: malformed flag value (missing =, empty side, bare =)
//   - ErrInvalidGlob: non-alphabet character in source or target
//   - ErrConflict: any pair of source patterns has overlapping match-sets
func Parse(flagValues []string) (*Map, error) {
	m := &Map{}
	for _, v := range flagValues {
		idx := strings.Index(v, "=")
		if idx < 0 {
			return nil, fmt.Errorf("%w: %q has no '='", ErrSyntax, v)
		}
		src, dst := v[:idx], v[idx+1:]
		if src == "" || dst == "" {
			return nil, fmt.Errorf("%w: %q has empty source or target", ErrSyntax, v)
		}
		if err := validateChars(src, "source"); err != nil {
			return nil, fmt.Errorf("%w: %v in %q", ErrInvalidGlob, err, v)
		}
		if err := validateCharsNoStar(dst, "target"); err != nil {
			return nil, fmt.Errorf("%w: %v in %q", ErrInvalidGlob, err, v)
		}
		r, err := compilePattern(src)
		if err != nil {
			// Unreachable: compilePattern always returns nil error; kept for
			// forward-compatibility if compilePattern gains validation.
			return nil, fmt.Errorf("%w: compile %q: %v", ErrInvalidGlob, src, err)
		}
		m.rules = append(m.rules, rule{source: src, target: dst, regex: r})
	}

	// Conflict check: O(N^2) over the rule pairs. N is small (~10).
	for i := 0; i < len(m.rules); i++ {
		for j := i + 1; j < len(m.rules); j++ {
			if patternsConflict(m.rules[i].regex, m.rules[j].regex) {
				return nil, fmt.Errorf("%w: %q and %q have overlapping match-sets",
					ErrConflict, m.rules[i].source, m.rules[j].source)
			}
		}
	}
	return m, nil
}

// Lookup returns (target, true) if any rule matches input; (input, false)
// otherwise. Pure function; safe for concurrent use.
func (m *Map) Lookup(input string) (string, bool) {
	for _, r := range m.rules {
		if r.regex.matches(input) {
			return r.target, true
		}
	}
	return input, false
}

// Empty reports whether the map has zero rules.
func (m *Map) Empty() bool {
	return len(m.rules) == 0
}

func validateChars(s, label string) error {
	for _, r := range s {
		if r == '*' {
			continue
		}
		if !strings.ContainsRune(allowedAlphabet, r) {
			return fmt.Errorf("%s contains %q", label, r)
		}
	}
	return nil
}

func validateCharsNoStar(s, label string) error {
	for _, r := range s {
		if !strings.ContainsRune(allowedAlphabet, r) {
			return fmt.Errorf("%s contains %q", label, r)
		}
	}
	return nil
}
