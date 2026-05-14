package shellalias

import (
	"path/filepath"
	"strings"
)

// posixShell implements Shell for bash and zsh. They share an emitter
// and quoter; only Name() and RcFile() differ.
type posixShell struct {
	name   string // "bash" | "zsh"
	rcBase string // ".bashrc" | ".zshrc"
}

func newBash() *posixShell { return &posixShell{name: "bash", rcBase: ".bashrc"} }
func newZsh() *posixShell  { return &posixShell{name: "zsh", rcBase: ".zshrc"} }

func (p *posixShell) Name() string { return p.name }

func (p *posixShell) AliasFile() string {
	return filepath.Join(resolveHome(), "aliases.sh")
}

func (p *posixShell) RcFile() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, p.rcBase), nil
}

func (p *posixShell) Quote(arg string) string { return posixQuote(arg) }

func (p *posixShell) EmitAlias(name string, payload []string) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString(`() { ccm launch`)
	for _, tok := range payload {
		b.WriteByte(' ')
		b.WriteString(posixQuote(tok))
	}
	b.WriteString(` "$@"; }`)
	return b.String()
}

// posixQuote wraps `s` in single quotes, escaping any embedded
// single quote as '\''. Empty string becomes ''. This is the standard
// POSIX single-token quoting strategy; round-trip safe under sh, bash,
// zsh.
func posixQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
