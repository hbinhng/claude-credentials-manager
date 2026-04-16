package share

import (
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
// Later tasks extend this with peer reload and OAuth refresh.
func (s *credState) Fresh() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cred.ClaudeAiOauth.AccessToken, nil
}
