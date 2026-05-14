package shellalias

import (
	"bytes"
	"errors"
	"testing"
)

var errKeyEOF = errors.New("key stream EOF")

// keyScript returns a closure that emits the given runes one at a time,
// then returns errKeyEOF.
func keyScript(rr ...rune) func() (rune, error) {
	i := 0
	return func() (rune, error) {
		if i >= len(rr) {
			return 0, errKeyEOF
		}
		r := rr[i]
		i++
		return r, nil
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestPrompt_DefaultsToHint(t *testing.T) {
	shells := []Shell{newZsh(), newBash(), newFish()}
	// hintIndex=0 (zsh pre-checked); user just presses Enter.
	keys := keyScript('\r')
	var w bytes.Buffer
	got, err := selectShellsWithIO(shells, 0, keys, &w)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name() != "zsh" {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_TogglesAndConfirms(t *testing.T) {
	shells := []Shell{newZsh(), newBash(), newFish()}
	// hintIndex=0. Sequence: down, space (toggle bash), enter.
	keys := keyScript('\x1b', '[', 'B', ' ', '\r')
	got, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{got[0].Name(), got[1].Name()}
	if !(contains(names, "zsh") && contains(names, "bash")) {
		t.Fatalf("got %+v", names)
	}
}

func TestPrompt_Cancel(t *testing.T) {
	shells := []Shell{newZsh()}
	keys := keyScript('\x1b') // ESC alone (not followed by [)
	_, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != ErrCancelled {
		t.Fatalf("got %v", err)
	}
}

func TestPrompt_NoShells(t *testing.T) {
	_, err := selectShellsWithIO(nil, 0, keyScript('\r'), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error on empty shell list")
	}
}

func TestPrompt_RequiresAtLeastOnePick(t *testing.T) {
	// hintIndex=-1 (no default); pressing enter with nothing checked is a no-op;
	// then space, then enter completes.
	shells := []Shell{newBash()}
	keys := keyScript('\r', ' ', '\r')
	got, err := selectShellsWithIO(shells, -1, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_ArrowUpDown(t *testing.T) {
	shells := []Shell{newBash(), newZsh(), newFish()}
	// Sequence: down, down (cursor at fish), up (cursor back at zsh), space, enter.
	keys := keyScript('\x1b', '[', 'B', '\x1b', '[', 'B', '\x1b', '[', 'A', ' ', '\r')
	got, err := selectShellsWithIO(shells, -1, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name() != "zsh" {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_VimKeys(t *testing.T) {
	shells := []Shell{newBash(), newZsh(), newFish()}
	// j j (down twice to fish), k (up once to zsh), space, enter.
	keys := keyScript('j', 'j', 'k', ' ', '\r')
	got, err := selectShellsWithIO(shells, -1, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name() != "zsh" {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_CursorClampedAtBounds(t *testing.T) {
	// Arrow up at top, arrow down at bottom — neither should panic or wrap.
	shells := []Shell{newBash(), newZsh()}
	keys := keyScript('\x1b', '[', 'A', // up at top (clamped)
		'\x1b', '[', 'B', '\x1b', '[', 'B', // down twice (second is clamped)
		'k', // vim up at top
		'j', 'j', // vim down twice (second is clamped)
		' ', '\r')
	got, err := selectShellsWithIO(shells, -1, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_CtrlC(t *testing.T) {
	shells := []Shell{newBash()}
	keys := keyScript(3) // Ctrl-C
	_, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != ErrCancelled {
		t.Fatalf("got %v", err)
	}
}

func TestPrompt_EscPartialSequence(t *testing.T) {
	// ESC followed by EOF (not '[') = cancel.
	shells := []Shell{newBash()}
	keys := keyScript('\x1b') // ESC alone; next call returns errKeyEOF
	_, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != ErrCancelled {
		t.Fatalf("got %v", err)
	}
}

func TestPrompt_EscArrowEOF(t *testing.T) {
	// ESC '[' then EOF on the arrow byte = cancel.
	shells := []Shell{newBash()}
	keys := keyScript('\x1b', '[')
	_, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != ErrCancelled {
		t.Fatalf("got %v", err)
	}
}

func TestPrompt_LFEnter(t *testing.T) {
	// '\n' should be treated like '\r'.
	shells := []Shell{newBash()}
	keys := keyScript('\n')
	got, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_UnknownKeyIgnored(t *testing.T) {
	// Random key 'x' should be ignored (loop continues).
	shells := []Shell{newBash()}
	keys := keyScript('x', '\r')
	got, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_HintIndexOutOfRange(t *testing.T) {
	// hintIndex=99 should be ignored (no pre-check).
	shells := []Shell{newBash()}
	keys := keyScript(' ', '\r') // user must explicitly check
	got, err := selectShellsWithIO(shells, 99, keys, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestPrompt_MainLoopEOF(t *testing.T) {
	// readKey returns EOF immediately (no keys at all) = cancel.
	shells := []Shell{newBash()}
	keys := keyScript() // empty; first call returns errKeyEOF
	_, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != ErrCancelled {
		t.Fatalf("got %v", err)
	}
}

func TestPrompt_EscNonBracket(t *testing.T) {
	// ESC followed by a non-'[' rune (with no error) = cancel.
	shells := []Shell{newBash()}
	keys := keyScript('\x1b', 'O') // ESC O (not ESC [)
	_, err := selectShellsWithIO(shells, 0, keys, &bytes.Buffer{})
	if err != ErrCancelled {
		t.Fatalf("got %v", err)
	}
}
