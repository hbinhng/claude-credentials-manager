package share

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// credState wraps a mutable credential with the on-disk bookkeeping
// needed to stay in sync with peer processes (other ccm share/launch
// sessions, manual ccm refresh). Fresh() is the single entry point
// both Proxy.getFreshToken and LocalProxy.getFreshToken delegate to.
type credState struct {
	mu    sync.Mutex
	cred  *store.Credential
	mtime time.Time // zero until Fresh() primes it from disk on first reload check
}

func newCredState(cred *store.Credential) *credState {
	return &credState{cred: cred}
}

// Fresh returns the current access token. Cheap path: if the in-memory
// credential is still valid and the on-disk file has not been written
// by a peer since we last looked, return the in-memory access token.
// Later tasks extend this with OAuth refresh.
func (s *credState) Fresh() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfPeerWrote()

	return s.cred.ClaudeAiOauth.AccessToken, nil
}

// reloadIfPeerWrote re-reads the credential from disk if the file's
// mtime has changed since the last observation. Errors are logged and
// swallowed — the caller falls back to in-memory state, which matches
// the existing non-fatal handling of store.Save failures elsewhere in
// this package.
func (s *credState) reloadIfPeerWrote() {
	info, err := os.Stat(store.CredPath(s.cred.ID))
	if err != nil {
		fmt.Fprintf(errLog(), "ccm: stat credential file: %v\n", err)
		return
	}
	if info.ModTime().Equal(s.mtime) {
		return
	}
	reloaded, err := store.Load(s.cred.ID)
	if err != nil {
		fmt.Fprintf(errLog(), "ccm: reload credential from disk: %v\n", err)
		return
	}
	s.cred = reloaded
	s.mtime = info.ModTime()
}
