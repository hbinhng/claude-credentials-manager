package shellalias

import (
	"os/exec"
	"sort"
	"testing"
)

func TestDetect_ReturnsBuiltins(t *testing.T) {
	// Replace the LookPath hook so we get deterministic output regardless
	// of what's actually installed on the test host.
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	available := map[string]bool{"bash": true, "zsh": true, "fish": false, "pwsh": false}
	lookPath = func(name string) (string, error) {
		if available[name] {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	got := Detect()
	if len(got) != 2 {
		t.Fatalf("got %d shells", len(got))
	}
	names := []string{got[0].Name(), got[1].Name()}
	sort.Strings(names)
	if names[0] != "bash" || names[1] != "zsh" {
		t.Fatalf("got %v", names)
	}
}

func TestDetect_CurrentShellHintAtIndexZero(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
	t.Setenv("SHELL", "/bin/zsh")
	got := Detect()
	if len(got) == 0 || got[0].Name() != "zsh" {
		t.Fatalf("zsh should be first; got %+v", got)
	}
}

func TestDetect_NoCurrentShellNoReorder(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
	t.Setenv("SHELL", "/bin/csh") // unknown
	got := Detect()
	if got[0].Name() != "bash" {
		t.Fatalf("expected default order (bash first), got %+v", got)
	}
}

func TestCurrentShellHint_EmptyShell(t *testing.T) {
	t.Setenv("SHELL", "")
	if got := currentShellHint(); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestCurrentShellHint_PwshFromShellEnv(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/pwsh")
	if got := currentShellHint(); got != "pwsh" {
		t.Fatalf("got %q", got)
	}
}

func TestDetect_NoShellsPresent(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(name string) (string, error) {
		return "", exec.ErrNotFound
	}
	got := Detect()
	if len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func TestDetect_HintAlreadyFirstNoOpSwap(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
	t.Setenv("SHELL", "/bin/bash") // bash is already first in default order
	got := Detect()
	if got[0].Name() != "bash" {
		t.Fatalf("expected bash first, got %+v", got)
	}
}
