package cmd

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/shellalias"
	"github.com/spf13/cobra"
)

func TestParseAliasArgs_Create_Minimal(t *testing.T) {
	got, err := parseAliasArgs([]string{"--as", "cld", "--load-balance", "cred-a"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := aliasArgs{
		mode:    aliasModeCreate,
		name:    "cld",
		payload: []string{"--load-balance", "cred-a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseAliasArgs_Create_WithDoubleDash(t *testing.T) {
	got, err := parseAliasArgs([]string{"--as", "cld", "--load-balance", "cred-a", "--", "-p", "hi"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := aliasArgs{
		mode:    aliasModeCreate,
		name:    "cld",
		payload: []string{"--load-balance", "cred-a", "--", "-p", "hi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseAliasArgs_Create_ShellsAnywhere(t *testing.T) {
	cases := [][]string{
		{"--as", "cld", "--shells", "bash,zsh", "--load-balance", "c"},
		{"--as", "cld", "--load-balance", "c", "--shells", "bash,zsh"},
		{"--shells", "bash,zsh", "--as", "cld", "--load-balance", "c"},
	}
	for i, argv := range cases {
		got, err := parseAliasArgs(argv)
		if err != nil {
			t.Fatalf("case %d err: %v", i, err)
		}
		if got.name != "cld" || !reflect.DeepEqual(got.shells, []string{"bash", "zsh"}) ||
			!reflect.DeepEqual(got.payload, []string{"--load-balance", "c"}) {
			t.Fatalf("case %d got %+v", i, got)
		}
	}
}

func TestParseAliasArgs_Create_Force(t *testing.T) {
	got, err := parseAliasArgs([]string{"--as", "cld", "--force", "--load-balance", "c"})
	if err != nil || !got.force {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParseAliasArgs_List(t *testing.T) {
	got, err := parseAliasArgs([]string{"--list"})
	if err != nil || got.mode != aliasModeList {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParseAliasArgs_Remove(t *testing.T) {
	got, err := parseAliasArgs([]string{"--remove", "cld"})
	if err != nil || got.mode != aliasModeRemove || got.name != "cld" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParseAliasArgs_Errors(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"no args", nil, "--as <name> is required"},
		{"--as without name", []string{"--as"}, "--as requires a value"},
		{"--shells without value", []string{"--as", "cld", "--shells", "--load-balance"}, "--shells requires"},
		{"--shells at end of argv", []string{"--as", "cld", "--shells"}, "--shells requires"},
		{"--list with extras", []string{"--list", "--as", "cld"}, "--list takes no other arguments"},
		{"--remove without name", []string{"--remove"}, "--remove requires a name"},
		{"--remove with extras", []string{"--remove", "cld", "--force"}, "--remove takes only a name"},
		{"--remove not first", []string{"--force", "--remove", "cld"}, "--remove takes only a name"},
		{"create without payload", []string{"--as", "cld"}, "nothing to capture"},
		{"create with bad name", []string{"--as", "1bad", "--load-balance", "c"}, "invalid alias name"},
		{"create with empty name", []string{"--as", "", "--load-balance", "c"}, "invalid alias name"},
		{"unknown shell", []string{"--as", "cld", "--shells", "csh", "--load-balance", "c"}, "unknown shell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAliasArgs(tc.argv)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err %v want substring %q", err, tc.want)
			}
		})
	}
}

// resetAliasHooks restores the dispatcher's test seams to their
// production defaults. Call from each TestAliasDispatch_* via t.Cleanup.
func resetAliasHooks() {
	aliasDetectFn = shellalias.Detect
	aliasInstallFn = shellalias.Install
	aliasListFn = shellalias.List
	aliasRemoveFn = shellalias.Remove
	aliasPromptFn = shellalias.SelectShells
	aliasIsTTYFn = func() bool { return false }
	aliasLookPathFn = exec.LookPath
}

// fakeBash is a Shell stub used by dispatch tests; the install hook is
// replaced so no real I/O happens through these methods.
type fakeBash struct{}

func (fakeBash) Name() string                      { return "bash" }
func (fakeBash) AliasFile() string                 { return "" }
func (fakeBash) RcFiles() ([]string, error)        { return []string{""}, nil }
func (fakeBash) EmitAlias(string, []string) string { return "" }
func (fakeBash) Quote(string) string               { return "" }

func TestAliasDispatch_Create_FlagSelectsShells(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)

	var capturedShells []string
	aliasInstallFn = func(name string, payload []string, targets []shellalias.Shell, force bool) []error {
		for _, sh := range targets {
			capturedShells = append(capturedShells, sh.Name())
		}
		return make([]error, len(targets))
	}
	aliasDetectFn = func() []shellalias.Shell { return nil }
	aliasIsTTYFn = func() bool { return false }

	var stdout bytes.Buffer
	err := runAlias(&stdout, &stdout, []string{
		"--as", "cld", "--shells", "bash,zsh", "--load-balance", "c",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(capturedShells) != 2 {
		t.Fatalf("got %v", capturedShells)
	}
}

func TestAliasDispatch_Create_NonTTY_DefaultsToHint(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)

	aliasDetectFn = func() []shellalias.Shell { return []shellalias.Shell{fakeBash{}} }
	aliasIsTTYFn = func() bool { return false }

	var called []string
	aliasInstallFn = func(name string, payload []string, targets []shellalias.Shell, force bool) []error {
		for _, sh := range targets {
			called = append(called, sh.Name())
		}
		return make([]error, len(targets))
	}
	err := runAlias(io.Discard, &bytes.Buffer{}, []string{"--as", "cld", "--load-balance", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(called) != 1 || called[0] != "bash" {
		t.Fatalf("got %v", called)
	}
}

func TestAliasDispatch_List_PrintsTable(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)

	aliasListFn = func() ([]shellalias.ListEntry, error) {
		return []shellalias.ListEntry{
			{Name: "cld", Shells: []string{"bash", "zsh"}, Payload: []string{"--load-balance", "cred-a"}},
			{Name: "cld-prod", Shells: []string{"pwsh"}, Payload: []string{"--via", "eyJrI..."}},
		}, nil
	}
	var out bytes.Buffer
	if err := runAlias(&out, &out, []string{"--list"}); err != nil {
		t.Fatal(err)
	}
	str := out.String()
	for _, want := range []string{"NAME", "SHELLS", "LAUNCH ARGS", "cld", "bash zsh", "--load-balance cred-a", "cld-prod", "pwsh", "--via <redacted>"} {
		if !strings.Contains(str, want) {
			t.Fatalf("missing %q in output:\n%s", want, str)
		}
	}
	// The raw ticket must never appear.
	if strings.Contains(str, "eyJrI...") {
		t.Fatalf("raw ticket leaked into output:\n%s", str)
	}
}

func TestRedactPayload(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, "(empty)"},
		{"no via", []string{"--load-balance", "cred-a"}, "--load-balance cred-a"},
		{"via only", []string{"--via", "secret"}, "--via <redacted>"},
		{"via mid", []string{"--rebalance-interval", "10m", "--via", "secret"}, "--rebalance-interval 10m --via <redacted>"},
		{"trailing via", []string{"--via"}, "--via"}, // no next token to redact
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactPayload(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestAliasDispatch_List_Empty(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasListFn = func() ([]shellalias.ListEntry, error) { return nil, nil }
	var out bytes.Buffer
	if err := runAlias(&out, &out, []string{"--list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no ccm aliases defined") {
		t.Fatalf("got %q", out.String())
	}
}

func TestAliasDispatch_Remove_Missing(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasRemoveFn = func(name string) error { return shellalias.ErrNotFound }
	err := runAlias(io.Discard, io.Discard, []string{"--remove", "missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("got %v", err)
	}
}

func TestAliasDispatch_Remove_Success(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasRemoveFn = func(name string) error { return nil }
	var out bytes.Buffer
	if err := runAlias(&out, &out, []string{"--remove", "cld"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed alias") {
		t.Fatalf("got %q", out.String())
	}
}

func TestAliasDispatch_Create_TTYPromptsForShells(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)

	aliasIsTTYFn = func() bool { return true }
	aliasDetectFn = func() []shellalias.Shell { return []shellalias.Shell{fakeBash{}} }
	aliasPromptFn = func(shells []shellalias.Shell, hint int) ([]shellalias.Shell, error) {
		// Simulate the user picking the detected default.
		return shells, nil
	}
	var installCalled bool
	aliasInstallFn = func(name string, payload []string, targets []shellalias.Shell, force bool) []error {
		installCalled = true
		return make([]error, len(targets))
	}
	if err := runAlias(io.Discard, io.Discard, []string{"--as", "cld", "--load-balance", "c"}); err != nil {
		t.Fatal(err)
	}
	if !installCalled {
		t.Fatal("install hook not called")
	}
}

func TestAliasDispatch_Create_NoShellsDetected(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasIsTTYFn = func() bool { return false }
	aliasDetectFn = func() []shellalias.Shell { return nil }
	err := runAlias(io.Discard, &bytes.Buffer{}, []string{"--as", "cld", "--load-balance", "c"})
	if err == nil || !strings.Contains(err.Error(), "no supported shells") {
		t.Fatalf("got %v", err)
	}
}

func TestAliasDispatch_Create_PromptCancelled(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasIsTTYFn = func() bool { return true }
	aliasDetectFn = func() []shellalias.Shell { return []shellalias.Shell{fakeBash{}} }
	aliasPromptFn = func(shells []shellalias.Shell, hint int) ([]shellalias.Shell, error) {
		return nil, shellalias.ErrCancelled
	}
	err := runAlias(io.Discard, io.Discard, []string{"--as", "cld", "--load-balance", "c"})
	if !errors.Is(err, shellalias.ErrCancelled) {
		t.Fatalf("got %v", err)
	}
}

func TestAliasDispatch_Create_ShellNotDetectedButRequested(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	// fish isn't in detected shells, but user asks for it via --shells.
	aliasDetectFn = func() []shellalias.Shell { return []shellalias.Shell{fakeBash{}} }
	var capturedNames []string
	aliasInstallFn = func(name string, payload []string, targets []shellalias.Shell, force bool) []error {
		for _, sh := range targets {
			capturedNames = append(capturedNames, sh.Name())
		}
		return make([]error, len(targets))
	}
	var stderr bytes.Buffer
	err := runAlias(io.Discard, &stderr, []string{"--as", "cld", "--shells", "fish", "--load-balance", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(capturedNames) != 1 || capturedNames[0] != "fish" {
		t.Fatalf("got %v", capturedNames)
	}
	if !strings.Contains(stderr.String(), "not detected on PATH") {
		t.Fatalf("missing warning: %s", stderr.String())
	}
}

func TestAliasDispatch_Create_InstallError(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasDetectFn = func() []shellalias.Shell { return []shellalias.Shell{fakeBash{}} }
	aliasInstallFn = func(name string, payload []string, targets []shellalias.Shell, force bool) []error {
		errs := make([]error, len(targets))
		errs[0] = errors.New("disk full")
		return errs
	}
	var stderr bytes.Buffer
	err := runAlias(io.Discard, &stderr, []string{"--as", "cld", "--load-balance", "c"})
	if err == nil {
		t.Fatal("expected install error to propagate")
	}
	if !strings.Contains(stderr.String(), "disk full") {
		t.Fatalf("missing error in stderr: %s", stderr.String())
	}
}

func TestAliasDispatch_ParseError_Propagates(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	err := runAlias(io.Discard, io.Discard, []string{"--as"})
	if err == nil || !strings.Contains(err.Error(), "--as requires a value") {
		t.Fatalf("got %v", err)
	}
}

func TestAliasDispatch_List_Error(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasListFn = func() ([]shellalias.ListEntry, error) {
		return nil, errors.New("disk error")
	}
	err := runAlias(io.Discard, io.Discard, []string{"--list"})
	if err == nil || !strings.Contains(err.Error(), "disk error") {
		t.Fatalf("got %v", err)
	}
}

func TestAliasDispatch_ShadowWarning(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasInstallFn = func(name string, payload []string, targets []shellalias.Shell, force bool) []error {
		return make([]error, len(targets))
	}
	aliasDetectFn = func() []shellalias.Shell { return []shellalias.Shell{fakeBash{}} }
	aliasLookPathFn = func(name string) (string, error) {
		if name == "ls" {
			return "/usr/bin/ls", nil
		}
		return "", errors.New("not found")
	}
	var stderr bytes.Buffer
	err := runAlias(io.Discard, &stderr, []string{"--as", "ls", "--load-balance", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "shadows existing binary") {
		t.Fatalf("missing shadow warning: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "/usr/bin/ls") {
		t.Fatalf("missing binary path: %q", stderr.String())
	}
}

func TestAliasDispatch_NoShadowWarningWhenAbsent(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasInstallFn = func(name string, payload []string, targets []shellalias.Shell, force bool) []error {
		return make([]error, len(targets))
	}
	aliasDetectFn = func() []shellalias.Shell { return []shellalias.Shell{fakeBash{}} }
	aliasLookPathFn = func(name string) (string, error) {
		return "", errors.New("not found")
	}
	var stderr bytes.Buffer
	err := runAlias(io.Discard, &stderr, []string{"--as", "novel-name", "--load-balance", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "shadows existing binary") {
		t.Fatalf("unexpected shadow warning: %q", stderr.String())
	}
}

func TestAliasComplete_FlagsWhenEmpty(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	got, dir := aliasComplete(nil, []string{}, "")
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive: %v", dir)
	}
	want := []string{"--as", "--shells", "--force", "--list", "--remove"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAliasComplete_FlagsWhenDashDash(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	got, _ := aliasComplete(nil, []string{}, "--")
	if len(got) != 5 {
		t.Fatalf("got %v", got)
	}
}

func TestAliasComplete_RemoveSuggestsAliasNames(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasListFn = func() ([]shellalias.ListEntry, error) {
		return []shellalias.ListEntry{
			{Name: "cld", Shells: []string{"bash"}},
			{Name: "cld-prod", Shells: []string{"pwsh"}},
		}, nil
	}
	got, dir := aliasComplete(nil, []string{"--remove"}, "")
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive: %v", dir)
	}
	want := []string{"cld", "cld-prod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAliasComplete_RemoveErrorReturnsNil(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	aliasListFn = func() ([]shellalias.ListEntry, error) {
		return nil, errors.New("disk error")
	}
	got, _ := aliasComplete(nil, []string{"--remove"}, "")
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestAliasComplete_ShellsSuggestsShellNames(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	got, _ := aliasComplete(nil, []string{"--shells"}, "")
	want := []string{"bash", "zsh", "fish", "pwsh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAliasComplete_AsReturnsNothing(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	got, _ := aliasComplete(nil, []string{"--as"}, "")
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestAliasComplete_PayloadTokenReturnsNothing(t *testing.T) {
	resetAliasHooks()
	t.Cleanup(resetAliasHooks)
	// Mid-payload typing — e.g. `ccm alias --as cld --load-balance cre<TAB>`.
	got, _ := aliasComplete(nil, []string{"--as", "cld", "--load-balance"}, "cre")
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
