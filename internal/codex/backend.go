// Package codex manages the active codex CLI credential by writing
// to ~/.codex/auth.json. Mirrors the surface of internal/claude.
package codex

// backend mirrors internal/claude.backend exactly.
type backend interface {
	Read() (blob []byte, ok bool, err error)
	Write(blob []byte) error
	Remove() error
}

// currentBackend always returns the file backend. Codex doesn't have
// a keychain dimension; the codex CLI only reads files.
func currentBackend() backend { return fileBackend{} }
