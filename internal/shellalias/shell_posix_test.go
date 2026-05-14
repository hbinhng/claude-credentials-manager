package shellalias

import (
	"errors"
	"strings"
	"testing"
)

func TestPOSIX_QuoteSimple(t *testing.T) {
	got := posixQuote("simple")
	if got != "'simple'" {
		t.Fatalf("got %q", got)
	}
}

func TestPOSIX_QuoteWithSingleQuote(t *testing.T) {
	got := posixQuote("it's")
	if got != `'it'\''s'` {
		t.Fatalf("got %q", got)
	}
}

func TestPOSIX_QuoteEmpty(t *testing.T) {
	got := posixQuote("")
	if got != "''" {
		t.Fatalf("got %q", got)
	}
}

func TestPOSIX_EmitAlias_NoArgs(t *testing.T) {
	s := newBash()
	got := s.EmitAlias("cld", nil)
	want := `cld() { ccm launch "$@"; }`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPOSIX_EmitAlias_LoadBalance(t *testing.T) {
	s := newBash()
	got := s.EmitAlias("cld", []string{"--load-balance", "cred-a", "cred-b"})
	want := `cld() { ccm launch '--load-balance' 'cred-a' 'cred-b' "$@"; }`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPOSIX_EmitAlias_WithDoubleDashPayload(t *testing.T) {
	s := newBash()
	got := s.EmitAlias("cld", []string{"--load-balance", "c", "--", "-p", "hi"})
	want := `cld() { ccm launch '--load-balance' 'c' '--' '-p' 'hi' "$@"; }`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPOSIX_Names(t *testing.T) {
	if newBash().Name() != "bash" || newZsh().Name() != "zsh" {
		t.Fatal("name mismatch")
	}
}

func TestPOSIX_Quote_MethodReceiver(t *testing.T) {
	// Covers (*posixShell).Quote which simply delegates to posixQuote.
	if got := newBash().Quote("x"); got != "'x'" {
		t.Fatalf("got %q", got)
	}
}

func TestPOSIX_AliasFile_RespectsCCMHome(t *testing.T) {
	t.Setenv("CCM_HOME", "/fake/ccm")
	if got := newBash().AliasFile(); got != "/fake/ccm/aliases.sh" {
		t.Fatalf("got %q", got)
	}
}

func TestPOSIX_RcFiles_BashAndZsh(t *testing.T) {
	bashRcs, err := newBash().RcFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(bashRcs) != 1 {
		t.Fatalf("bash: expected 1 rc path, got %d: %v", len(bashRcs), bashRcs)
	}
	if !strings.HasSuffix(bashRcs[0], ".bashrc") {
		t.Fatalf("bash rc: %q", bashRcs[0])
	}

	zshRcs, err := newZsh().RcFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(zshRcs) != 1 {
		t.Fatalf("zsh: expected 1 rc path, got %d: %v", len(zshRcs), zshRcs)
	}
	if !strings.HasSuffix(zshRcs[0], ".zshrc") {
		t.Fatalf("zsh rc: %q", zshRcs[0])
	}
}

func TestPOSIX_RcFiles_UserHomeDirError(t *testing.T) {
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := newBash().RcFiles(); err == nil {
		t.Fatal("expected error")
	}
}
