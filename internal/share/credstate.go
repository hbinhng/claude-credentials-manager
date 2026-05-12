package share

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
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

func newCredState(cred *store.Credential) (*credState, error) {
	// Provider guard removed: both providers are first-class. The
	// per-provider field divergence is hidden behind cred.AccessToken() /
	// cred.ExpiresAtMillis() / cred.SetTokens() (see store.Credential
	// accessors added in Task 2).
	return &credState{cred: cred}, nil
}

// Fresh returns the current access token. Cheap path: if the in-memory
// cred isn't expired and a peer hasn't written, return immediately.
// Slow path: delegate to credflow, which takes its own cross-process
// flock and dispatches per provider (claude / codex). Codex's rotating
// refresh-token model is handled inside credflow.
func (s *credState) Fresh() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfPeerWrote()

	if !s.cred.IsExpired() && !s.cred.IsExpiringSoon() {
		return s.cred.AccessToken(), nil
	}

	fresh, err := credflow.RefreshFn(s.cred.ID)
	if err != nil {
		return "", fmt.Errorf("refresh: %w", err)
	}
	s.cred = fresh
	if info, statErr := os.Stat(store.CredPath(s.cred.ID)); statErr == nil {
		s.mtime = info.ModTime()
	}
	return s.cred.AccessToken(), nil
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

// Compile-time check that *credState satisfies tokenSource.
var _ tokenSource = (*credState)(nil)

// Compile-time check that *credState satisfies poolEntryState.
var _ poolEntryState = (*credState)(nil)

func (s *credState) credID() string             { return s.cred.ID }
func (s *credState) credName() string           { return s.cred.Name }
func (s *credState) credExpiresAt() time.Time   { return time.UnixMilli(s.cred.ExpiresAtMillis()) }
func (s *credState) credPtr() *store.Credential { return s.cred }
func (s *credState) upstreamURL() string        { return upstreamBase() }
func (s *credState) isPassthrough() bool        { return false }
