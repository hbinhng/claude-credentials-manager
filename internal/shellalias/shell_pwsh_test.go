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
	want := `function cld { ccm launch '--load-balance' 'c' -- @args }`
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

func TestPwsh_RcFiles_DefaultResolverErrors(t *testing.T) {
	// Exercises the package-default pwshResolver, which is intentionally
	// an error until detect_windows.go wires the real Windows resolver.
	orig := pwshResolver
	t.Cleanup(func() { pwshResolver = orig })
	// Don't replace — call through to the existing default.
	if _, err := newPwsh().RcFiles(); err == nil {
		t.Fatal("expected error from default resolver")
	}
}

func TestPwsh_RcFiles_ConfiguredResolverReturnsPath(t *testing.T) {
	orig := pwshResolver
	t.Cleanup(func() { pwshResolver = orig })
	pwshResolver = func() ([]string, error) {
		return []string{`C:\Users\u\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`}, nil
	}
	rcs, err := newPwsh().RcFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rcs) != 1 {
		t.Fatalf("expected 1 path, got %d: %v", len(rcs), rcs)
	}
	if !strings.HasSuffix(rcs[0], "Microsoft.PowerShell_profile.ps1") {
		t.Fatalf("got %q", rcs[0])
	}
}

func TestPwsh_RcFiles_ReturnsAllProfiles(t *testing.T) {
	orig := pwshResolver
	t.Cleanup(func() { pwshResolver = orig })
	pwshResolver = func() ([]string, error) {
		return []string{
			`C:\Users\u\Documents\PowerShell\Microsoft.PowerShell_profile.ps1`,
			`C:\Users\u\Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1`,
		}, nil
	}
	rcs, err := newPwsh().RcFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rcs) != 2 {
		t.Fatalf("got %d paths, want 2: %v", len(rcs), rcs)
	}
}

func TestPwsh_QuoteMultipleSingleQuotes(t *testing.T) {
	// Verifies ReplaceAll doubles every ' not just the first.
	if got := pwshQuote("a'b'c"); got != "'a''b''c'" {
		t.Fatalf("got %q", got)
	}
}

func TestPwsh_EmitAlias_NoArgs(t *testing.T) {
	got := newPwsh().EmitAlias("cld", nil)
	want := `function cld { ccm launch -- @args }`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
