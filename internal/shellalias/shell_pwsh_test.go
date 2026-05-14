package shellalias

import (
	"strings"
	"testing"
)

func TestPwsh_QuoteSimple(t *testing.T) {
	if got := pwshQuote("simple"); got != "'simple'" {
		t.Fatalf("got %q", got)
	}
}

func TestPwsh_QuoteSingleQuote(t *testing.T) {
	// PS literal-string rule: '' inside '...' is a single '
	if got := pwshQuote("it's"); got != `'it''s'` {
		t.Fatalf("got %q", got)
	}
}

func TestPwsh_QuoteEmpty(t *testing.T) {
	if got := pwshQuote(""); got != "''" {
		t.Fatalf("got %q", got)
	}
}

func TestPwsh_EmitAlias(t *testing.T) {
	s := newPwsh()
	got := s.EmitAlias("cld", []string{"--load-balance", "c"})
	want := `function cld { ccm launch '--load-balance' 'c' @args }`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if s.Name() != "pwsh" {
		t.Fatal("name mismatch")
	}
}

func TestPwsh_Quote_MethodReceiver(t *testing.T) {
	if got := newPwsh().Quote("x"); got != "'x'" {
		t.Fatalf("got %q", got)
	}
}

func TestPwsh_AliasFile_RespectsCCMHome(t *testing.T) {
	t.Setenv("CCM_HOME", "/fake/ccm")
	want := "/fake/ccm/aliases.ps1"
	if got := newPwsh().AliasFile(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPwsh_RcFile_DefaultResolverErrors(t *testing.T) {
	// Exercises the package-default pwshResolver, which is intentionally
	// an error until Task 9 wires the real Windows resolver.
	orig := pwshResolver
	t.Cleanup(func() { pwshResolver = orig })
	// Don't replace — call through to the existing default.
	if _, err := newPwsh().RcFile(); err == nil {
		t.Fatal("expected error from default resolver")
	}
}

func TestPwsh_RcFile_ConfiguredResolverReturnsPath(t *testing.T) {
	orig := pwshResolver
	t.Cleanup(func() { pwshResolver = orig })
	pwshResolver = func() (string, error) {
		return `C:\Users\u\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`, nil
	}
	got, err := newPwsh().RcFile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "Microsoft.PowerShell_profile.ps1") {
		t.Fatalf("got %q", got)
	}
}
