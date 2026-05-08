package alias_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
)

func TestParse_ValidSingleRule(t *testing.T) {
	m, err := alias.Parse([]string{"claude-opus-*=gpt-5-codex"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Empty() {
		t.Error("map should not be empty")
	}
}

func TestParse_ValidMultipleRules(t *testing.T) {
	m, err := alias.Parse([]string{
		"claude-opus-*=gpt-5-codex",
		"claude-haiku-*=gpt-5-mini",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, _ := m.Lookup("claude-opus-4.7"); got != "gpt-5-codex" {
		t.Errorf("opus lookup = %q, want gpt-5-codex", got)
	}
	if got, _ := m.Lookup("claude-haiku-4"); got != "gpt-5-mini" {
		t.Errorf("haiku lookup = %q, want gpt-5-mini", got)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	m, err := alias.Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if !m.Empty() {
		t.Error("nil input should produce empty map")
	}
	if got, ok := m.Lookup("claude-opus-4.7"); ok {
		t.Errorf("Lookup on empty map returned (%q, true), want false", got)
	}
}

func TestParse_SyntaxErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"missing equals", "claude-opus-gpt-5", alias.ErrSyntax},
		{"empty source", "=gpt-5", alias.ErrSyntax},
		{"empty target", "claude-opus-*=", alias.ErrSyntax},
		{"only equals", "=", alias.ErrSyntax},
		{"invalid char in source", "claude/*=gpt-5", alias.ErrInvalidGlob},
		{"invalid char in target", "claude-*=gpt 5", alias.ErrInvalidGlob},
		{"question mark in source", "claude-?-opus=gpt-5", alias.ErrInvalidGlob},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := alias.Parse([]string{c.input})
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestParse_ConflictRejected(t *testing.T) {
	_, err := alias.Parse([]string{
		"claude-*=gpt-5",
		"claude-opus-*=gpt-5-codex",
	})
	if !errors.Is(err, alias.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "claude-*") || !strings.Contains(err.Error(), "claude-opus-*") {
		t.Errorf("ErrConflict message must name both patterns; got: %v", err)
	}
}

func TestLookup_NoMatch(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-opus-*=gpt-5-codex"})
	got, ok := m.Lookup("gemini-pro")
	if ok {
		t.Errorf("Lookup(gemini-pro) ok = true, want false")
	}
	if got != "gemini-pro" {
		t.Errorf("Lookup(gemini-pro) = %q, want gemini-pro (passthrough)", got)
	}
}

func TestLookup_ExactLiteralPattern(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-opus-4.7=gpt-5-codex"})
	if got, ok := m.Lookup("claude-opus-4.7"); !ok || got != "gpt-5-codex" {
		t.Errorf("exact match: got=%q ok=%v, want gpt-5-codex true", got, ok)
	}
	if _, ok := m.Lookup("claude-opus-4.6"); ok {
		t.Error("non-matching literal should not match")
	}
}

func TestLookup_WildcardPrefix(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-*=gpt-5"})
	tests := []struct {
		input string
		match bool
	}{
		{"claude-opus-4.7", true},
		{"claude-haiku", true},
		{"claude-", true},
		{"gpt-4", false},
		{"", false},
	}
	for _, tc := range tests {
		got, ok := m.Lookup(tc.input)
		if ok != tc.match {
			t.Errorf("Lookup(%q) match = %v, want %v", tc.input, ok, tc.match)
		}
		if tc.match && got != "gpt-5" {
			t.Errorf("Lookup(%q) target = %q, want gpt-5", tc.input, got)
		}
	}
}

func TestLookup_WildcardSuffix(t *testing.T) {
	m, _ := alias.Parse([]string{"*-codex=gpt-5-codex"})
	if got, ok := m.Lookup("anything-codex"); !ok || got != "gpt-5-codex" {
		t.Errorf("got=%q ok=%v", got, ok)
	}
	if _, ok := m.Lookup("not-it"); ok {
		t.Error("non-matching suffix should not match")
	}
}

func TestLookup_WildcardMiddle(t *testing.T) {
	m, _ := alias.Parse([]string{"claude-*-opus=gpt-5-codex"})
	if _, ok := m.Lookup("claude-4.7-opus"); !ok {
		t.Error("claude-4.7-opus should match claude-*-opus")
	}
	if _, ok := m.Lookup("claude-opus"); ok {
		t.Error("claude-opus (no middle) should not match claude-*-opus")
	}
}

func TestLookup_UniversalWildcard(t *testing.T) {
	m, _ := alias.Parse([]string{"*=fallback-model"})
	if got, ok := m.Lookup("any-model-name"); !ok || got != "fallback-model" {
		t.Errorf("universal: got=%q ok=%v", got, ok)
	}
	if got, ok := m.Lookup(""); !ok || got != "fallback-model" {
		t.Errorf("universal on empty: got=%q ok=%v", got, ok)
	}
}
