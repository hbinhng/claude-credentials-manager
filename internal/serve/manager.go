// Package serve implements the HTTP dashboard backing `ccm serve`.
//
// The Manager type holds a set of live share.Session handles keyed by
// credential ID. It does not speak HTTP — that is web.go's job — and
// it does not own proxy/tunnel lifecycle — that is share.StartSession's
// job. Manager is the glue: "start one", "stop one", "list all",
// "tear everything down on shutdown".
package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// ErrAlreadyStarted is returned by Manager.Start when a session for
// the given credential ID is already live.
var ErrAlreadyStarted = errors.New("session already started for this credential")

// Manager owns the set of live share sessions for a running ccm serve.
type Manager struct {
	starter share.SessionStarter
	errLog  io.Writer

	mu       sync.Mutex
	sessions map[string]share.Session
}

// NewManager returns a Manager that starts sessions via starter.
// errLog defaults to os.Stderr when nil.
func NewManager(starter share.SessionStarter, errLog io.Writer) *Manager {
	if errLog == nil {
		errLog = os.Stderr
	}
	return &Manager{
		starter:  starter,
		errLog:   errLog,
		sessions: make(map[string]share.Session),
	}
}

// Start creates a new share session for cred with the given options.
// Returns ErrAlreadyStarted if a session for cred.ID is already live.
//
// Start is careful about the race window between the pre-check and
// the store: the starter may block, so we release the lock while it
// runs and double-check afterwards. If a concurrent Start for the
// same cred won, the second caller stops the session it started and
// returns ErrAlreadyStarted.
func (m *Manager) Start(cred *store.Credential, opts share.Options) (share.Session, error) {
	m.mu.Lock()
	if _, exists := m.sessions[cred.ID]; exists {
		m.mu.Unlock()
		return nil, ErrAlreadyStarted
	}
	m.mu.Unlock()

	sess, err := m.starter.StartSession(cred, opts)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if _, exists := m.sessions[cred.ID]; exists {
		m.mu.Unlock()
		_ = sess.Stop()
		return nil, ErrAlreadyStarted
	}
	m.sessions[cred.ID] = sess
	m.mu.Unlock()
	return sess, nil
}

// Get returns the live session for credID, if any.
func (m *Manager) Get(credID string) (share.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[credID]
	return s, ok
}

// Stop terminates the session for credID if one is running. No-op
// for unknown credID (returns nil).
func (m *Manager) Stop(credID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[credID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.sessions, credID)
	m.mu.Unlock()
	return sess.Stop()
}

// List returns all live sessions sorted by CredID.
func (m *Manager) List() []share.Session {
	m.mu.Lock()
	out := make([]share.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CredID() < out[j].CredID() })
	return out
}

// Shutdown stops every live session in parallel with a 5-second
// per-session timeout (or until ctx is cancelled, whichever is
// sooner). Returns an aggregate error listing every non-nil Stop()
// result; per-session errors are also logged to errLog.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	sessions := make(map[string]share.Session, len(m.sessions))
	maps.Copy(sessions, m.sessions)
	m.sessions = make(map[string]share.Session)
	m.mu.Unlock()

	type result struct {
		id  string
		err error
	}
	results := make(chan result, len(sessions))
	for id, s := range sessions {
		id, s := id, s
		go func() {
			perCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- s.Stop() }()

			select {
			case err := <-done:
				results <- result{id, err}
			case <-perCtx.Done():
				results <- result{id, fmt.Errorf("stop timeout: %w", perCtx.Err())}
			}
		}()
	}

	var errs []string
	for i := 0; i < len(sessions); i++ {
		r := <-results
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.id, r.err))
			fmt.Fprintf(m.errLog, "ccm serve: session %s stop: %v\n", r.id, r.err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("shutdown: %s", strings.Join(errs, "; "))
}
