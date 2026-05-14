package shellalias

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// ErrCancelled is returned by SelectShells when the user pressed ESC
// or Ctrl-C. The CLI maps this to exit code 130 with no files written.
var ErrCancelled = errors.New("ccm alias: cancelled by user")

// SelectShells draws an interactive multi-select listing `shells` with
// the element at `hintIndex` pre-checked. Returns the chosen subset.
// `stdin` must be a TTY; the caller is responsible for that check.
func SelectShells(shells []Shell, hintIndex int) ([]Shell, error) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// coverage: unreachable in unit tests (requires a real tty)
		return nil, fmt.Errorf("raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)
	return selectShellsWithIO(shells, hintIndex, readRawKey, os.Stderr)
}

// selectShellsWithIO is the testable core. `readKey` is a producer of
// runes (one ANSI escape byte at a time); `out` receives render output.
func selectShellsWithIO(shells []Shell, hintIndex int, readKey func() (rune, error), out io.Writer) ([]Shell, error) {
	if len(shells) == 0 {
		return nil, errors.New("no shells detected; pass --shells to install anyway")
	}
	checked := make([]bool, len(shells))
	if hintIndex >= 0 && hintIndex < len(shells) {
		checked[hintIndex] = true
	}
	cursor := 0
	for {
		render(out, shells, checked, cursor)
		r, err := readKey()
		if err != nil {
			return nil, ErrCancelled
		}
		switch r {
		case '\r', '\n':
			var picked []Shell
			for i, c := range checked {
				if c {
					picked = append(picked, shells[i])
				}
			}
			if len(picked) == 0 {
				continue // require at least one
			}
			return picked, nil
		case ' ':
			checked[cursor] = !checked[cursor]
		case '\x1b':
			// ESC alone = cancel; ESC [ A/B = arrow keys.
			next, err := readKey()
			if err != nil || next != '[' {
				return nil, ErrCancelled
			}
			arrow, err := readKey()
			if err != nil {
				return nil, ErrCancelled
			}
			switch arrow {
			case 'A': // up
				if cursor > 0 {
					cursor--
				}
			case 'B': // down
				if cursor < len(shells)-1 {
					cursor++
				}
			}
		case 'k':
			if cursor > 0 {
				cursor--
			}
		case 'j':
			if cursor < len(shells)-1 {
				cursor++
			}
		case 3: // Ctrl-C
			return nil, ErrCancelled
		}
	}
}

func render(out io.Writer, shells []Shell, checked []bool, cursor int) {
	fmt.Fprint(out, "\x1b[2J\x1b[H") // clear screen, home cursor
	fmt.Fprintln(out, "ccm: where should the alias be installed?")
	for i, sh := range shells {
		mark := " "
		if checked[i] {
			mark = "x"
		}
		prefix := "  "
		if i == cursor {
			prefix = "> "
		}
		rc, _ := sh.RcFile()
		fmt.Fprintf(out, "%s[%s] %-8s %s\r\n", prefix, mark, sh.Name(), rc)
	}
	fmt.Fprintln(out, "\r\nspace = toggle, enter = confirm, esc = cancel")
}

// readRawKey reads a single byte from stdin and casts to rune. Used in
// production; tests inject their own producer.
func readRawKey() (rune, error) {
	var buf [1]byte
	if _, err := os.Stdin.Read(buf[:]); err != nil {
		// coverage: unreachable in unit tests (requires a real tty)
		return 0, err
	}
	return rune(buf[0]), nil
}
