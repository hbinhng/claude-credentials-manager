package claude

import (
	"bytes"
	"errors"
	"os"
)

// stubBlob is the placeholder blob written by EnsureFileStub. It is
// structurally valid JSON in the shape claude parses (claudeAiOauth
// wrapper) but the tokens are obvious placeholders that cannot
// authenticate. expiresAt is far in the future to keep claude's
// startup path away from any refresh logic.
//
// Deliberately omits ccmSourceId so the stub never registers as
// ccm-managed; combined with the cleanup returned by EnsureFileStub,
// which removes the file only when its bytes are still byte-identical
// to this constant, the stub leaves no permanent trace.
var stubBlob = []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-ccm-share-capture-stub","refreshToken":"sk-ant-ort01-ccm-share-capture-stub","expiresAt":4102444800000,"scopes":["user:inference"],"subscriptionType":"pro"}}`)

// EnsureFileStub writes a placeholder ~/.claude/.credentials.json
// when no entry exists at that path, and returns a cleanup func that
// removes the file iff its contents are still byte-identical to what
// we wrote.
//
// When any file (regular, dir, or symlink — broken or not) already
// exists at the path, EnsureFileStub is a no-op and the returned
// cleanup is a no-op.
//
// Intended for ccm share's identity-capture step: `claude -p` refuses
// to start without a credentials file, but the share proxy returns a
// synthetic 401 in CAPTURE mode and never forwards anything upstream,
// so the stub's bogus tokens never leak. The cleanup runs immediately
// after capture so the filesystem state observed before/after
// `ccm share` is unchanged on hosts that already had a credential and
// gains/loses a single ephemeral file on hosts that did not.
//
// Returns an error only when statting fails for a reason other than
// "not found" or when the subsequent write fails.
func EnsureFileStub() (cleanup func(), err error) {
	path := credentialsPath()
	if _, statErr := os.Lstat(path); statErr == nil {
		return func() {}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return func() {}, statErr
	}
	if err := os.WriteFile(path, stubBlob, 0600); err != nil {
		return func() {}, err
	}
	return func() {
		cur, rerr := os.ReadFile(path)
		if rerr != nil {
			return
		}
		if !bytes.Equal(cur, stubBlob) {
			return
		}
		_ = os.Remove(path)
	}, nil
}
