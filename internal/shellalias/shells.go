// Package shellalias installs, lists, and removes ccm-managed shell
// aliases across bash, zsh, fish, and PowerShell. It owns one alias
// file per shell flavor under $CCM_HOME and a single sentinel-fenced
// source/dot snippet inside each user's rc file.
package shellalias

// Shell is one target shell flavor.
type Shell interface {
	Name() string                                   // "bash" | "zsh" | "fish" | "pwsh"
	AliasFile() string                              // absolute path under $CCM_HOME
	RcFile() (string, error)                        // absolute path to the rc we modify
	EmitAlias(name string, payload []string) string // function body w/ proper quoting
	Quote(arg string) string                        // single-token quoter
}
