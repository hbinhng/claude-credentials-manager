package shellalias

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned by Remove when no shell file contained the
// requested alias.
var ErrNotFound = errors.New("alias not found")

const payloadPrefix = "# ccm-alias:payload:"

// ListEntry is one ccm-managed alias as returned by List.
// Each entry aggregates across all shell alias files that hold the
// alias: a single alias installed for both bash and fish appears as
// one entry with Shells = ["bash", "fish"].
type ListEntry struct {
	Name    string   // alias name
	Shells  []string // shell flavors carrying this alias (e.g. ["bash", "zsh"])
	Payload []string // captured ccm launch args, or nil if absent
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
	functionBody := sh.EmitAlias(name, payload)
	body, err := buildAliasBody(payload, functionBody)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	updated := upsertAliasBlock(content, name, body)
	if err := os.WriteFile(aliasPath, updated, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", aliasPath, err)
	}

	rcPaths, err := sh.RcFiles()
	if err != nil {
		return fmt.Errorf("resolve %s rc: %w", sh.Name(), err)
	}
	for _, rcPath := range rcPaths {
		if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
			return fmt.Errorf("create rc dir for %s (%s): %w", sh.Name(), rcPath, err)
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
	}
	return nil
}

// buildAliasBody joins the payload comment with the function definition
// emitted by the Shell. The payload comment is always the first line of
// the block body so extractPayload can find it deterministically.
func buildAliasBody(payload []string, functionBody string) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		// coverage: unreachable — []string always marshals.
		return "", err
	}
	return payloadPrefix + string(encoded) + "\n" + functionBody, nil
}

// extractPayload reads the JSON payload comment from a block body.
// Returns nil if the body lacks the comment (e.g. block was installed
// by an older ccm version, or the comment was hand-edited away).
func extractPayload(body string) []string {
	lines := strings.SplitN(body, "\n", 2)
	if len(lines) == 0 {
		return nil
	}
	first := lines[0]
	if !strings.HasPrefix(first, payloadPrefix) {
		return nil
	}
	jsonPart := strings.TrimPrefix(first, payloadPrefix)
	var payload []string
	if err := json.Unmarshal([]byte(jsonPart), &payload); err != nil {
		return nil
	}
	return payload
}

// List reads every alias file under $CCM_HOME and returns one entry
// per alias name, aggregating which shells carry it. The aggregation
// is deterministic: shells appear in canonical order bash, zsh, fish,
// pwsh. Payload comes from the first block (by shell-iteration order)
// that carries a parsable payload comment; if no block has one, the
// entry's Payload is nil.
//
// bash+zsh share aliases.sh, so an alias only present there appears
// with Shells = ["bash", "zsh"].
func List() ([]ListEntry, error) {
	type seen struct {
		shells  []string
		payload []string
	}
	bag := map[string]*seen{}
	order := []string{} // first-appearance ordering for stable output

	type shellFile struct {
		name string
		path string
		// pairedWith is the name of a second shell that consumes the
		// same alias file (used to record both bash+zsh for aliases.sh
		// without reading the file twice).
		pairedWith string
	}
	// Iterate distinct files: aliases.sh (bash+zsh), aliases.fish, aliases.ps1.
	files := []shellFile{
		{name: "bash", path: newBash().AliasFile(), pairedWith: "zsh"},
		{name: "fish", path: newFish().AliasFile()},
		{name: "pwsh", path: newPwsh().AliasFile()},
	}
	for _, f := range files {
		content, err := readOrEmpty(f.path)
		if err != nil {
			return nil, err
		}
		for _, b := range parseAliasBlocks(content) {
			s, ok := bag[b.Name]
			if !ok {
				s = &seen{}
				bag[b.Name] = s
				order = append(order, b.Name)
			}
			s.shells = append(s.shells, f.name)
			if f.pairedWith != "" {
				s.shells = append(s.shells, f.pairedWith)
			}
			if s.payload == nil {
				s.payload = extractPayload(b.Body)
			}
		}
	}

	out := make([]ListEntry, 0, len(order))
	for _, name := range order {
		s := bag[name]
		out = append(out, ListEntry{
			Name:    name,
			Shells:  s.shells,
			Payload: s.payload,
		})
	}
	return out, nil
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
