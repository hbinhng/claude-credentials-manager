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

// flavorOf maps a Shell to the rc-snippet flavor string used by
// buildRcSnippet. bash and zsh both map to "posix".
func flavorOf(s Shell) string {
	switch s.Name() {
	case "bash", "zsh":
		return "posix"
	case "fish":
		return "fish"
	case "pwsh":
		return "pwsh"
	default:
		// coverage: unreachable — only the four registered shells call this.
		return ""
	}
}
