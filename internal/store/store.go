package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccm")
}

func EnsureDir() error {
	return os.MkdirAll(Dir(), 0700)
}

func CredPath(id string) string {
	return filepath.Join(Dir(), id+".credentials.json")
}

func Save(cred *Credential) error {
	if err := EnsureDir(); err != nil {
		return fmt.Errorf("create ~/.ccm: %w", err)
	}
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}
	tmp := CredPath(cred.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, CredPath(cred.ID))
}

func Load(id string) (*Credential, error) {
	data, err := os.ReadFile(CredPath(id))
	if err != nil {
		return nil, err
	}
	var cred Credential
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("parse %s: %w", id, err)
	}
	return &cred, nil
}

func Delete(id string) error {
	return os.Remove(CredPath(id))
}

func List() ([]*Credential, error) {
	pattern := filepath.Join(Dir(), "*.credentials.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var creds []*Credential
	for _, path := range matches {
		base := filepath.Base(path)
		id := strings.TrimSuffix(base, ".credentials.json")
		if id == "" || strings.HasSuffix(id, ".tmp") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot read %s: %v\n", base, err)
			continue
		}
		var cred Credential
		if err := json.Unmarshal(data, &cred); err != nil {
			fmt.Fprintf(os.Stderr, "warning: corrupt %s: %v\n", base, err)
			continue
		}
		creds = append(creds, &cred)
	}
	return creds, nil
}
