package shellalias

import (
	"errors"
	"path/filepath"
	"strings"
)

// pwshResolver returns the absolute paths to the PowerShell profiles to
// modify. Replaced in tests; in production wired in detect_windows.go.
var pwshResolver func() ([]string, error) = func() ([]string, error) {
	return nil, errors.New("pwsh profile resolution not configured")
}

// pwshShell implements Shell for PowerShell (5.1+ compatible).
type pwshShell struct{}

func newPwsh() *pwshShell { return &pwshShell{} }

func (pwshShell) Name() string { return "pwsh" }

func (pwshShell) AliasFile() string {
	return filepath.Join(resolveHome(), "aliases.ps1")
}

func (pwshShell) RcFiles() ([]string, error) { return pwshResolver() }

func (pwshShell) Quote(arg string) string { return pwshQuote(arg) }

func (pwshShell) EmitAlias(name string, payload []string) string {
	var b strings.Builder
	b.WriteString("function ")
	b.WriteString(name)
	b.WriteString(" { ccm launch")
	for _, tok := range payload {
		b.WriteByte(' ')
		b.WriteString(pwshQuote(tok))
	}
	b.WriteString(" @args }")
	return b.String()
}

// pwshQuote single-quotes a token using PowerShell literal-string
// rules: ' becomes '' inside the wrapping '…'. Compatible with PS 5.1+.
func pwshQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
