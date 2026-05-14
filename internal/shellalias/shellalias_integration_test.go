//go:build unix

package shellalias

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFakeCcm compiles a tiny Go program that prints its argv joined
// by "|" so the test can assert the forwarded slice. The fake replaces
// the real `ccm` on PATH during the test.
func buildFakeCcm(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	src := `package main
import ("fmt"; "os"; "strings")
func main() {
	fmt.Println("CCM-ARGV: " + strings.Join(os.Args[1:], "|"))
}`
	srcPath := filepath.Join(d, "main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(d, "ccm")
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binPath
}

func TestIntegration_POSIX(t *testing.T) {
	for _, shellBin := range []string{"bash", "zsh"} {
		t.Run(shellBin, func(t *testing.T) {
			if _, err := exec.LookPath(shellBin); err != nil {
				t.Skipf("%s not on PATH", shellBin)
			}
			runPOSIXIntegration(t, shellBin)
		})
	}
}

func runPOSIXIntegration(t *testing.T, shellBin string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	t.Setenv("HOME", home)

	fakeCcm := buildFakeCcm(t)

	var sh Shell
	if shellBin == "bash" {
		sh = newBash()
	} else {
		sh = newZsh()
	}
	if errs := Install("cld", []string{"--load-balance", "c"}, []Shell{sh}, false); errs[0] != nil {
		t.Fatal(errs[0])
	}

	rcPath, _ := sh.RcFile()
	script := "source " + rcPath + " && cld extra-arg"
	cmd := exec.Command(shellBin, "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(fakeCcm)+":"+os.Getenv("PATH"),
		"CCM_HOME="+home,
		"HOME="+home,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", shellBin, err, out)
	}
	want := "CCM-ARGV: launch|--load-balance|c|extra-arg"
	if !strings.Contains(string(out), want) {
		t.Fatalf("got %q want substring %q", out, want)
	}
}

func TestIntegration_Fish(t *testing.T) {
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not on PATH")
	}
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	fakeCcm := buildFakeCcm(t)
	sh := newFish()
	if errs := Install("cld", []string{"--load-balance", "c"}, []Shell{sh}, false); errs[0] != nil {
		t.Fatal(errs[0])
	}
	rcPath, _ := sh.RcFile()
	cmd := exec.Command("fish", "-c", "source "+rcPath+"; cld extra-arg")
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(fakeCcm)+":"+os.Getenv("PATH"),
		"CCM_HOME="+home,
		"HOME="+home,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fish: %v\n%s", err, out)
	}
	want := "CCM-ARGV: launch|--load-balance|c|extra-arg"
	if !strings.Contains(string(out), want) {
		t.Fatalf("got %q want substring %q", out, want)
	}
}

// TestIntegration_DoubleDashPreserved verifies the design's key
// property: the captured `--` reaches launch verbatim, with use-time
// args appended after it. This is the load-bearing behavior for
// "pre-bake claude args at create time" — the failure mode here is
// "the alias works but extra-arg lands in launch's flag namespace
// instead of claude's."
func TestIntegration_DoubleDashPreserved(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	t.Setenv("HOME", home)
	fakeCcm := buildFakeCcm(t)
	sh := newBash()
	if errs := Install("cld",
		[]string{"--load-balance", "c", "--", "-p", "default"},
		[]Shell{sh}, false); errs[0] != nil {
		t.Fatal(errs[0])
	}
	rcPath, _ := sh.RcFile()
	cmd := exec.Command("bash", "-c", "source "+rcPath+" && cld --verbose")
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(fakeCcm)+":"+os.Getenv("PATH"),
		"CCM_HOME="+home,
		"HOME="+home,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "CCM-ARGV: launch|--load-balance|c|--|-p|default|--verbose"
	if !strings.Contains(string(out), want) {
		t.Fatalf("got %q want substring %q", out, want)
	}
}
