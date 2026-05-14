// Package shellalias defines the Shell interface and per-flavor
// implementations that emit ccm alias definitions for a target shell.
package shellalias

// Shell is one target shell flavor.
type Shell interface {
	Name() string                                   // "bash" | "zsh" | "fish" | "pwsh"
	AliasFile() string                              // absolute path under $CCM_HOME
	RcFile() (string, error)                        // absolute path to the rc we modify
	EmitAlias(name string, payload []string) string // function body w/ proper quoting
	Quote(arg string) string                        // single-token quoter
}
