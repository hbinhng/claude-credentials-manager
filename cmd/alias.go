package cmd

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

type aliasMode int

const (
	aliasModeCreate aliasMode = iota
	aliasModeList
	aliasModeRemove
)

type aliasArgs struct {
	mode    aliasMode
	name    string   // required for create + remove
	shells  []string // empty = "prompt or detect"
	force   bool
	payload []string // create-only; verbatim ccm launch args
}

var aliasNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)

var validShellNames = map[string]struct{}{
	"bash": {}, "zsh": {}, "fish": {}, "pwsh": {},
}

// parseAliasArgs is the manual argv walker. Cobra's DisableFlagParsing is
// true on aliasCmd so flags like --load-balance in the payload don't
// collide with cobra's parser. We consume only our own five flags
// (--as, --shells, --force, --list, --remove) and capture everything
// else verbatim into payload. A literal `--` is preserved in payload
// because ccm launch itself uses `--` as the launch/claude separator.
func parseAliasArgs(argv []string) (aliasArgs, error) {
	var out aliasArgs
	out.mode = aliasModeCreate

	// First pass: detect --list / --remove which are exclusive.
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--list":
			if len(argv) != 1 {
				return out, errors.New("--list takes no other arguments")
			}
			out.mode = aliasModeList
			return out, nil
		case "--remove":
			if len(argv) == 1 {
				return out, errors.New("--remove requires a name")
			}
			if i != 0 || len(argv) != 2 {
				return out, errors.New("--remove takes only a name; got extra arguments")
			}
			out.mode = aliasModeRemove
			out.name = argv[1]
			return out, nil
		}
	}

	// Create flow: walk, consume our flags, append rest to payload.
	asProvided := false
	i := 0
	for i < len(argv) {
		switch argv[i] {
		case "--as":
			if i+1 >= len(argv) {
				return out, errors.New("--as requires a value")
			}
			out.name = argv[i+1]
			asProvided = true
			i += 2
		case "--shells":
			if i+1 >= len(argv) {
				return out, errors.New("--shells requires a value")
			}
			raw := argv[i+1]
			if strings.HasPrefix(raw, "--") || raw == "" {
				return out, errors.New("--shells requires a value")
			}
			for _, s := range strings.Split(raw, ",") {
				s = strings.TrimSpace(s)
				if _, ok := validShellNames[s]; !ok {
					return out, fmt.Errorf("unknown shell %q (valid: bash, zsh, fish, pwsh)", s)
				}
				out.shells = append(out.shells, s)
			}
			i += 2
		case "--force":
			out.force = true
			i++
		default:
			out.payload = append(out.payload, argv[i])
			i++
		}
	}

	if !asProvided {
		return out, errors.New("--as <name> is required")
	}
	if !aliasNameRe.MatchString(out.name) {
		return out, fmt.Errorf("invalid alias name %q; must match [A-Za-z_][A-Za-z0-9_-]*", out.name)
	}
	if len(out.payload) == 0 {
		return out, errors.New("nothing to capture; pass at least one ccm launch arg")
	}
	return out, nil
}

var aliasCmd = &cobra.Command{
	Use:                "alias --as <name> <ccm launch args...> | --list | --remove <name>",
	Short:              "Create, list, or remove a shell alias for `ccm launch`",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := parseAliasArgs(args)
		if err != nil {
			return err
		}
		// Dispatch wired in Task 11.
		return errors.New("ccm alias: not yet implemented")
	},
}

func init() {
	rootCmd.AddCommand(aliasCmd)
}
