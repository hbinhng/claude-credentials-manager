package shellalias

import (
	"bytes"
	"fmt"
)

const (
	rcBeginSentinel = "# ccm-aliases:begin"
	rcEndSentinel   = "# ccm-aliases:end"
)

// buildRcSnippet returns the per-flavor block to insert into a user's
// rc file. The alias-file path is baked in at install time so the
// snippet does not depend on $CCM_HOME being set at shell-startup.
//
// flavor is one of "posix" (bash + zsh), "fish", "pwsh".
func buildRcSnippet(flavor, aliasFilePath string) string {
	switch flavor {
	case "posix":
		return fmt.Sprintf(
			"%s (managed by `ccm alias`; do not edit)\n"+
				"[ -f %q ] && . %q\n"+
				"%s\n",
			rcBeginSentinel, aliasFilePath, aliasFilePath, rcEndSentinel,
		)
	case "fish":
		return fmt.Sprintf(
			"%s\n"+
				"test -f %q; and source %q\n"+
				"%s\n",
			rcBeginSentinel, aliasFilePath, aliasFilePath, rcEndSentinel,
		)
	case "pwsh":
		return fmt.Sprintf(
			"%s\n"+
				"if (Test-Path '%s') { . '%s' }\n"+
				"%s\n",
			rcBeginSentinel, aliasFilePath, aliasFilePath, rcEndSentinel,
		)
	default:
		// coverage: unreachable — only the three flavors above call this.
		return ""
	}
}

// hasRcSentinel reports whether the rc file already contains a
// ccm-managed block.
func hasRcSentinel(content []byte) bool {
	return bytes.Contains(content, []byte(rcBeginSentinel))
}

// ensureRcSnippet returns rc content guaranteed to contain a
// ccm-managed block. If one is already present (sentinel match),
// returns the input unchanged. If absent, appends the freshly-built
// snippet to the end (separated by a newline if content does not end
// in one).
func ensureRcSnippet(content []byte, flavor, aliasFilePath string) []byte {
	if hasRcSentinel(content) {
		return content
	}
	snippet := buildRcSnippet(flavor, aliasFilePath)
	if len(content) == 0 {
		return []byte(snippet)
	}
	var b bytes.Buffer
	b.Write(content)
	if !bytes.HasSuffix(content, []byte("\n")) {
		b.WriteByte('\n')
	}
	b.WriteString(snippet)
	return b.Bytes()
}
