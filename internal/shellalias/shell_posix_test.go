package shellalias

import "testing"

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
