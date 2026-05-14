package shellalias

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveHome_CCMHomeWins(t *testing.T) {
	t.Setenv("CCM_HOME", "/explicit/path")
	if got := resolveHome(); got != "/explicit/path" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveHome_DefaultsToHomeDotCcm(t *testing.T) {
	t.Setenv("CCM_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	want := filepath.Join(home, ".ccm")
	if got := resolveHome(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveHome_WhitespaceCCMHomeFallsBack(t *testing.T) {
	t.Setenv("CCM_HOME", "   ")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	want := filepath.Join(home, ".ccm")
	if got := resolveHome(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveHome_UserHomeDirError_FallsBackToRelative(t *testing.T) {
	t.Setenv("CCM_HOME", "")
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	if got := resolveHome(); got != ".ccm" {
		t.Fatalf("got %q want %q", got, ".ccm")
	}
}
