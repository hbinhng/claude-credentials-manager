package share

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
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
// If the token is expired or expiring soon, an exclusive cross-process
// flock is acquired before refreshing. After acquiring the lock a
// double-check reload is performed — a peer may have already refreshed
// while we were blocked, in which case we skip the OAuth call entirely.
func (s *credState) Fresh() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfPeerWrote()

	if !s.cred.IsExpired() && !s.cred.IsExpiringSoon() {
		return s.cred.ClaudeAiOauth.AccessToken, nil
	}

	err := withLock(s.cred.ID, func() error {
		// Double-check after acquiring the lock: a peer may have
		// refreshed while we were blocked. If the reloaded credential
		// is now fresh, skip our own OAuth call entirely.
		s.reloadIfPeerWrote()
		if !s.cred.IsExpired() && !s.cred.IsExpiringSoon() {
			return nil
		}
		return s.refreshLocked()
	})
	if err != nil {
		return "", err
	}
	return s.cred.ClaudeAiOauth.AccessToken, nil
}

// refreshLocked runs the OAuth refresh round-trip and persists the new
// tokens. Caller must hold s.mu and the cross-process flock (via
// withLock).
func (s *credState) refreshLocked() error {
	tokens, err := oauth.Refresh(s.cred.ClaudeAiOauth.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	s.cred.ClaudeAiOauth.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		s.cred.ClaudeAiOauth.RefreshToken = tokens.RefreshToken
	}
	s.cred.ClaudeAiOauth.ExpiresAt = time.Now().UnixMilli() + tokens.ExpiresIn*1000
	if scopes := strings.Fields(tokens.Scope); len(scopes) > 0 {
		s.cred.ClaudeAiOauth.Scopes = scopes
	}
	s.cred.LastRefreshedAt = time.Now().UTC().Format(time.RFC3339)
	if err := store.Save(s.cred); err != nil {
		fmt.Fprintf(errLog(), "ccm: warning: failed to persist refreshed credential: %v\n", err)
		return nil
	}
	if info, err := os.Stat(store.CredPath(s.cred.ID)); err == nil {
		s.mtime = info.ModTime()
	}
	return nil
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
func (s *credState) credExpiresAt() time.Time   { return time.UnixMilli(s.cred.ClaudeAiOauth.ExpiresAt) }
func (s *credState) credPtr() *store.Credential { return s.cred }
