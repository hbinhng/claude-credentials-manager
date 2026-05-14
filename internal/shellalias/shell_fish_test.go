package shellalias

import (
	"errors"
	"path/filepath"
	"strings"
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

func TestFish_RcFiles_AndUserHomeDirError(t *testing.T) {
	// happy path: returns a one-element slice ending in .config/fish/config.fish
	rcs, err := newFish().RcFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rcs) != 1 {
		t.Fatalf("expected 1 rc path, got %d: %v", len(rcs), rcs)
	}
	want := filepath.Join(".config", "fish", "config.fish")
	if !strings.HasSuffix(rcs[0], want) {
		t.Fatalf("got %q, want suffix %q", rcs[0], want)
	}

	// error path via seam (same pattern as TestPOSIX_RcFiles_UserHomeDirError)
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := newFish().RcFiles(); err == nil {
		t.Fatal("expected error")
	}
}

func TestFish_QuoteEmpty(t *testing.T) {
	if got := fishQuote(""); got != "''" {
		t.Fatalf("got %q", got)
	}
}

func TestFish_QuoteCombined(t *testing.T) {
	// Proves '\' is escaped before "'": if the order were reversed,
	// `a\'b` would be double-escaped instead of becoming `'a\\\'b'`.
	if got := fishQuote(`a\'b`); got != `'a\\\'b'` {
		t.Fatalf("got %q", got)
	}
}
