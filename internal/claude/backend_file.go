package claude

import (
	"errors"
	"os"
)

// fileBackend stores the blob in ~/.claude/.credentials.json as a plain
// regular file. Always available — no probe required.
type fileBackend struct{}

func (fileBackend) Read() ([]byte, bool, error) {
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

func (fileBackend) Write(blob []byte) error {
	tmp := credentialsPath() + ".tmp"
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, credentialsPath())
}

func (fileBackend) Remove() error {
	if err := os.Remove(credentialsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
