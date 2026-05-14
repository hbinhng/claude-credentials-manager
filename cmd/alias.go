package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/hbinhng/claude-credentials-manager/internal/shellalias"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// --- argv parser (preserved from Task 1) ---

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
	i := 0
	asProvided := false
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

	if !asProvided && out.name == "" {
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

// --- dispatch hooks (replaceable in tests) ---

var (
	aliasDetectFn   = shellalias.Detect
	aliasInstallFn  = shellalias.Install
	aliasListFn     = shellalias.List
	aliasRemoveFn   = shellalias.Remove
	aliasPromptFn   = shellalias.SelectShells
	aliasIsTTYFn    = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	aliasLookPathFn = exec.LookPath
)

func runAlias(stdout, stderr io.Writer, argv []string) error {
	a, err := parseAliasArgs(argv)
	if err != nil {
		return err
	}
	switch a.mode {
	case aliasModeList:
		return runAliasList(stdout)
	case aliasModeRemove:
		if err := aliasRemoveFn(a.name); err != nil {
			return fmt.Errorf("ccm alias: %s: %w", a.name, err)
		}
		fmt.Fprintf(stdout, "removed alias %q\n", a.name)
		return nil
	default:
		return runAliasCreate(stdout, stderr, a)
	}
}

func runAliasList(out io.Writer) error {
	entries, err := aliasListFn()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "no ccm aliases defined")
		return nil
	}
	w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSHELLS\tLAUNCH ARGS")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			e.Name,
			strings.Join(e.Shells, " "),
			redactPayload(e.Payload),
		)
	}
	return w.Flush()
}

// redactPayload renders captured ccm-launch args as a single
// space-joined string, censoring sensitive values. Today the only
// censored field is the token after --via (the share ticket carries
// an embedded access token).
func redactPayload(args []string) string {
	if len(args) == 0 {
		return "(empty)"
	}
	out := make([]string, len(args))
	censorNext := false
	for i, tok := range args {
		switch {
		case censorNext:
			out[i] = "<redacted>"
			censorNext = false
		case tok == "--via":
			out[i] = tok
			censorNext = true
		default:
			out[i] = tok
		}
	}
	return strings.Join(out, " ")
}

// launchValueFlags are `ccm launch` flags that consume one positional value.
// Keep in sync with the flags registered in cmd/launch.go's init().
var launchValueFlags = map[string]struct{}{
	"--via":                {},
	"--rebalance-interval": {},
	"--model-alias":        {},
}

// launchBoolFlags are `ccm launch` flags that are pure switches.
// --load-balance is followed by positional credential names (zero or more)
// which we treat as launch-side positional args until we hit a recognized
// claude-bound token.
var launchBoolFlags = map[string]struct{}{
	"--load-balance": {},
}

// inferLaunchClaudeBoundary returns the payload with `--` injected before
// the first token that looks like a claude flag — i.e., a `--<x>` that is
// not a known `ccm launch` flag and is not the value of a value-taking
// launch flag. If the payload already contains `--`, it is returned
// unchanged.
//
// This compensates for PowerShell silently dropping `--` from
// native-command argv. The launch flag set is duplicated from
// cmd/launch.go's init; keep in sync when launch gains a new flag.
func inferLaunchClaudeBoundary(payload []string) []string {
	if len(payload) == 0 {
		return payload
	}
	for _, t := range payload {
		if t == "--" {
			return payload // user-supplied -- present; nothing to do
		}
	}
	out := make([]string, 0, len(payload)+1)
	i := 0
	for i < len(payload) {
		tok := payload[i]
		if _, ok := launchValueFlags[tok]; ok {
			if i+1 < len(payload) {
				out = append(out, tok, payload[i+1])
				i += 2
				continue
			}
			out = append(out, tok)
			i++
			continue
		}
		if _, ok := launchBoolFlags[tok]; ok {
			out = append(out, tok)
			i++
			continue
		}
		if strings.HasPrefix(tok, "--") {
			// Unknown --flag → claude side starts here.
			out = append(out, "--")
			out = append(out, payload[i:]...)
			return out
		}
		// Positional arg (likely a credential name for --load-balance) —
		// still launch-side.
		out = append(out, tok)
		i++
	}
	return out
}

func runAliasCreate(stdout, stderr io.Writer, a aliasArgs) error {
	a.payload = inferLaunchClaudeBoundary(a.payload)
	targets, err := resolveTargets(stderr, a.shells)
	if err != nil {
		return err
	}
	if path, err := aliasLookPathFn(a.name); err == nil {
		fmt.Fprintf(stderr, "ccm alias: %q shadows existing binary at %s\n", a.name, path)
	}
	errs := aliasInstallFn(a.name, a.payload, targets, a.force)
	var firstErr error
	for i, e := range errs {
		if e != nil {
			fmt.Fprintf(stderr, "ccm alias: %s: %v\n", targets[i].Name(), e)
			if firstErr == nil {
				firstErr = e
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name()
	}
	fmt.Fprintf(stdout, "installed alias %q for %s\n", a.name, strings.Join(names, ", "))
	fmt.Fprintln(stdout, "open a new terminal (or `source` your rc) to use it")
	return nil
}

func resolveTargets(stderr io.Writer, requested []string) ([]shellalias.Shell, error) {
	detected := aliasDetectFn()
	if len(requested) > 0 {
		byName := map[string]shellalias.Shell{}
		for _, sh := range detected {
			byName[sh.Name()] = sh
		}
		var out []shellalias.Shell
		for _, name := range requested {
			sh, ok := byName[name]
			if !ok {
				sh = shellByName(name)
				fmt.Fprintf(stderr, "ccm alias: %s not detected on PATH; file will be written anyway\n", name)
			}
			out = append(out, sh)
		}
		return out, nil
	}
	if len(detected) == 0 {
		return nil, errors.New("ccm alias: no supported shells detected (pass --shells)")
	}
	if aliasIsTTYFn() {
		picked, err := aliasPromptFn(detected, 0)
		if err != nil {
			return nil, err
		}
		return picked, nil
	}
	fmt.Fprintf(stderr, "ccm alias: stdin not a tty; defaulting to %s (pass --shells to override)\n", detected[0].Name())
	return detected[:1], nil
}

func shellByName(name string) shellalias.Shell {
	for _, sh := range []shellalias.Shell{
		shellalias.NewBash(), shellalias.NewZsh(),
		shellalias.NewFish(), shellalias.NewPwsh(),
	} {
		if sh.Name() == name {
			return sh
		}
	}
	// coverage: unreachable — argv parser rejects unknown names earlier.
	return nil
}

// aliasComplete provides shell tab-completion for `ccm alias`. Because
// aliasCmd uses DisableFlagParsing, cobra's automatic flag completion
// is disabled; this function fills in the gap.
//
// It is also the seam that lets `--remove <TAB>` enumerate installed
// alias names — read through aliasListFn so tests can stub it.
func aliasComplete(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Value-taking flag just consumed → suggest its values.
	if len(args) > 0 {
		switch args[len(args)-1] {
		case "--remove":
			return aliasCompletionNames(), cobra.ShellCompDirectiveNoFileComp
		case "--shells":
			return []string{"bash", "zsh", "fish", "pwsh"}, cobra.ShellCompDirectiveNoFileComp
		case "--as":
			// User-supplied name; nothing useful to suggest.
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
	// Completing a flag-like token: offer the five known flags.
	if strings.HasPrefix(toComplete, "--") || toComplete == "" {
		return []string{"--as", "--shells", "--force", "--list", "--remove"}, cobra.ShellCompDirectiveNoFileComp
	}
	// Anything else is part of the captured ccm-launch payload; no
	// useful completion is possible from this layer.
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// aliasCompletionNames returns the names of installed aliases for
// `--remove <TAB>`. Routed through the aliasListFn seam so dispatch
// tests can stub it without touching the filesystem.
func aliasCompletionNames() []string {
	entries, err := aliasListFn()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// --- cobra wiring ---

var aliasCmd = &cobra.Command{
	Use:                "alias --as <name> <ccm launch args...> | --list | --remove <name>",
	Short:              "Create, list, or remove a shell alias for `ccm launch`",
	Long:               aliasLongDescription,
	DisableFlagParsing: true,
	ValidArgsFunction:  aliasComplete,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAlias(os.Stdout, os.Stderr, args)
	},
}

const aliasLongDescription = `Create, list, or remove a shell alias bound to a captured 'ccm launch'
invocation.

EXAMPLES

  # Bind 'cld' to a load-balance pool:
  ccm alias --as cld --load-balance cred-a cred-b

  # Bind to a remote ticket:
  ccm alias --as cld-prod --via eyJrI...

  # Append claude args at use time:
  cld -- -p "hello"

  # Pre-bake claude args at create time:
  ccm alias --as cld --load-balance cred-a -- -p "hi"

  # List installed aliases:
  ccm alias --list

  # Remove one:
  ccm alias --remove cld

FLAGS

  --as <name>       Alias name (required for create). Must match
                    [A-Za-z_][A-Za-z0-9_-]*.
  --shells <list>   Comma-separated subset of bash,zsh,fish,pwsh. If
                    omitted on a TTY, an interactive picker runs;
                    non-TTY falls back to the detected current shell.
  --force           Reserved for future use (currently a no-op). Will
                    eventually let you override shadow-binary warnings
                    without prompting.
  --list            List installed aliases. Mutually exclusive with
                    create flags.
  --remove <name>   Remove an installed alias. Mutually exclusive with
                    create flags.

NOTES

  * No '--' separator is required between 'ccm alias' flags and the
    captured payload. A literal '--' inside the payload is preserved
    because 'ccm launch' uses it to separate launch flags from claude
    args.
  * Supported shells: bash, zsh, fish, PowerShell (5.1+). Other shells
    (CMD, nushell, etc.) are not supported.
  * On macOS, bash login shells read ~/.bash_profile (not ~/.bashrc).
    If you use bash on macOS, source ~/.bashrc from ~/.bash_profile
    or run with --shells zsh (the macOS default).
  * Files written: $CCM_HOME/aliases.{sh,fish,ps1} and a sentinel-
    fenced block in your shell's rc file. The block sources the
    aliases file; subsequent 'ccm alias' invocations only mutate the
    aliases file, never re-touch the rc.
  * If you move $CCM_HOME, re-run 'ccm alias --as ...' once so the
    baked path in your rc snippet regenerates.`

func init() {
	rootCmd.AddCommand(aliasCmd)
}
