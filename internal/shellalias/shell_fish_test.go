package shellalias

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestFish_QuoteSimple(t *testing.T) {
	if got := fishQuote("simple"); got != "'simple'" {
		t.Fatalf("got %q", got)
	}
}

func TestFish_QuoteBackslash(t *testing.T) {
	// fish single-quote rules: backslash escapes \ and ' only.
	if got := fishQuote(`a\b`); got != `'a\\b'` {
		t.Fatalf("got %q", got)
	}
}

func TestFish_QuoteSingleQuote(t *testing.T) {
	if got := fishQuote(`a'b`); got != `'a\'b'` {
		t.Fatalf("got %q", got)
	}
}

func TestFish_EmitAlias(t *testing.T) {
	s := newFish()
	got := s.EmitAlias("cld", []string{"--load-balance", "c"})
	want := `function cld; ccm launch '--load-balance' 'c' $argv; end`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if s.Name() != "fish" {
		t.Fatal("name mismatch")
	}
}

func TestFish_Quote_MethodReceiver(t *testing.T) {
	if got := newFish().Quote("x"); got != "'x'" {
		t.Fatalf("got %q", got)
	}
}

func TestFish_AliasFile_RespectsCCMHome(t *testing.T) {
	t.Setenv("CCM_HOME", "/fake/ccm")
	if got := newFish().AliasFile(); got != "/fake/ccm/aliases.fish" {
		t.Fatalf("got %q", got)
	}
}

func TestFish_RcFile_AndUserHomeDirError(t *testing.T) {
	// happy path: returns a path ending in .config/fish/config.fish
	got, err := newFish().RcFile()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "config.fish" {
		t.Fatalf("got %q", got)
	}

	// error path via seam (same pattern as TestPOSIX_RcFile_UserHomeDirError)
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := newFish().RcFile(); err == nil {
		t.Fatal("expected error")
	}
}

func TestFish_QuoteEmpty(t *testing.T) {
	if got := fishQuote(""); got != "''" {
		t.Fatalf("got %q", got)
	}
}
