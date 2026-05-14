package shellalias

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Remove when no shell file contained the
// requested alias.
var ErrNotFound = errors.New("ccm alias: alias not found")

// ListEntry is one ccm-managed alias as returned by List.
type ListEntry struct {
	Name  string // alias name
	Shell string // "bash" | "zsh" | "fish" | "pwsh"
	Body  string // verbatim function definition (for diagnostic display)
}

// Install writes `name` with captured `payload` into every shell in
// `targets`. Returns one error per target slot; nil means success for
// that target. `force` is reserved for future use (e.g. overriding
// shadow warnings); not consulted today.
func Install(name string, payload []string, targets []Shell, force bool) []error {
	errs := make([]error, len(targets))
	for i, sh := range targets {
		errs[i] = installOne(sh, name, payload)
	}
	return errs
}

func installOne(sh Shell, name string, payload []string) error {
	aliasPath := sh.AliasFile()
	if err := os.MkdirAll(filepath.Dir(aliasPath), 0o755); err != nil {
		return fmt.Errorf("create %s dir: %w", sh.Name(), err)
	}
	content, err := readOrEmpty(aliasPath)
	if err != nil {
		return err
	}
	body := sh.EmitAlias(name, payload)
	updated := upsertAliasBlock(content, name, body)
	if err := os.WriteFile(aliasPath, updated, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", aliasPath, err)
	}

	rcPath, err := sh.RcFile()
	if err != nil {
		return fmt.Errorf("resolve %s rc: %w", sh.Name(), err)
	}
	if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
		return fmt.Errorf("create rc dir for %s: %w", sh.Name(), err)
	}
	rc, err := readOrEmpty(rcPath)
	if err != nil {
		return err
	}
	newRc := ensureRcSnippet(rc, flavorOf(sh), aliasPath)
	if string(newRc) != string(rc) {
		if err := os.WriteFile(rcPath, newRc, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", rcPath, err)
		}
	}
	return nil
}

// List reads every alias file under $CCM_HOME and returns all managed
// blocks. Files that don't exist are skipped. Order: bash → zsh → fish
// → pwsh, then declaration order within a file. bash + zsh share
// aliases.sh; dedupe by (name, body) to avoid double-reporting.
func List() ([]ListEntry, error) {
	var out []ListEntry
	for _, sh := range []Shell{newBash(), newZsh(), newFish(), newPwsh()} {
		path := sh.AliasFile()
		content, err := readOrEmpty(path)
		if err != nil {
			return nil, err
		}
		for _, b := range parseAliasBlocks(content) {
			out = append(out, ListEntry{Name: b.Name, Shell: sh.Name(), Body: b.Body})
		}
	}
	seen := map[string]bool{}
	deduped := out[:0]
	for _, e := range out {
		key := e.Name + "\x00" + e.Body
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, e)
	}
	return deduped, nil
}

// Remove deletes `name` from every shell alias file under $CCM_HOME.
// Returns ErrNotFound if no file contained it. bash + zsh share
// aliases.sh; iterating bash alone suffices for that file.
func Remove(name string) error {
	found := false
	for _, sh := range []Shell{newBash(), newFish(), newPwsh()} {
		path := sh.AliasFile()
		content, err := readOrEmpty(path)
		if err != nil {
			return err
		}
		updated, ok := removeAliasBlock(content, name)
		if !ok {
			continue
		}
		found = true
		if err := os.WriteFile(path, updated, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// readOrEmpty returns the file's bytes or empty if the file does not
// exist. Other errors surface as-is.
func readOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}
