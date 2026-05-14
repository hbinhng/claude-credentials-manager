package shellalias

import (
	"path/filepath"
	"strings"
)

// fishShell implements Shell for fish.
type fishShell struct{}

func newFish() *fishShell { return &fishShell{} }

func (fishShell) Name() string { return "fish" }

func (fishShell) AliasFile() string {
	return filepath.Join(resolveHome(), "aliases.fish")
}

func (fishShell) RcFiles() ([]string, error) {
	home, err := userHomeDir()
	if err != nil {
		// coverage: unreachable on supported OSes
		return nil, err
	}
	return []string{filepath.Join(home, ".config", "fish", "config.fish")}, nil
}

func (fishShell) Quote(arg string) string { return fishQuote(arg) }

func (fishShell) EmitAlias(name string, payload []string) string {
	var b strings.Builder
	b.WriteString("function ")
	b.WriteString(name)
	b.WriteString("; ccm launch")
	for _, tok := range payload {
		b.WriteByte(' ')
		b.WriteString(fishQuote(tok))
	}
	b.WriteString(" $argv; end")
	return b.String()
}

// fishQuote single-quotes a token. Inside single quotes, fish treats
// only `\` and `'` as escape-significant. Everything else is literal.
func fishQuote(s string) string {
	if s == "" {
		return "''"
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}
