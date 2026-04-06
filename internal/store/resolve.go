package store

import (
	"fmt"
	"strings"
)

// Resolve finds a credential by exact UUID, UUID prefix (min 4 chars), or name.
func Resolve(idOrName string) (*Credential, error) {
	creds, err := List()
	if err != nil {
		return nil, err
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("no credentials found in ~/.ccm/")
	}

	// Exact UUID match
	for _, c := range creds {
		if c.ID == idOrName {
			return c, nil
		}
	}

	// Exact name match
	for _, c := range creds {
		if c.Name == idOrName {
			return c, nil
		}
	}

	// UUID prefix match (min 4 chars)
	if len(idOrName) >= 4 {
		var matches []*Credential
		for _, c := range creds {
			if strings.HasPrefix(c.ID, idOrName) {
				matches = append(matches, c)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = fmt.Sprintf("  %s (%s)", m.ID[:8], m.Name)
			}
			return nil, fmt.Errorf("ambiguous prefix %q matches:\n%s", idOrName, strings.Join(ids, "\n"))
		}
	}

	return nil, fmt.Errorf("no credential matching %q", idOrName)
}
