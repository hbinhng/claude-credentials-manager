package shellalias

import (
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

func TestPOSIX_RcFile_BashAndZsh(t *testing.T) {
	bash, err := newBash().RcFile()
	if err != nil {
		t.Fatal(err)
	}
	zsh, err := newZsh().RcFile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(bash, ".bashrc") {
		t.Fatalf("bash rc: %q", bash)
	}
	if !strings.HasSuffix(zsh, ".zshrc") {
		t.Fatalf("zsh rc: %q", zsh)
	}
}
