package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
	"github.com/hbinhng/claude-credentials-manager/internal/share/alias"
)

// TestShareCommand_ModelAliasConflictRejected verifies that conflicting
// --model-alias patterns (overlapping source globs) are rejected at
// parse time before any session setup is attempted.
func TestShareCommand_ModelAliasConflictRejected(t *testing.T) {
	// "claude-*" and "claude-opus-*" overlap: "claude-opus-4" matches both.
	_, err := alias.Parse([]string{"claude-*=gpt-5", "claude-opus-*=gpt-4"})
	if err == nil {
		t.Fatal("want conflict error for overlapping alias patterns, got nil")
	}
	if !strings.Contains(err.Error(), "overlap") {
		t.Errorf("err = %v; want 'overlap' in message", err)
	}
}

// runSessionLoop must not NPE when cred is nil (passthrough-only).
func TestRunSessionLoopWithNilCred(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runSessionLoop panicked: %v", r)
		}
	}()
	sess := &fakeSession{
		ticket: "TKT",
		reach:  "https://r.example",
		mode:   "tunnel",
		credID: "fake",
		done:   make(chan struct{}),
	}
	close(sess.done)

	if err := runSessionLoop(sess, nil); err != nil {
		t.Errorf("runSessionLoop: %v", err)
	}
}

// fakeSession implements share.Session for nil-cred tests in this
// package. Placed here (not in share package) so it can be customized
// per test without polluting share package's public test seams.
type fakeSession struct {
	ticket, reach, mode, credID string
	done                        chan struct{}
}

func (f *fakeSession) Ticket() string         { return f.ticket }
func (f *fakeSession) CredID() string         { return f.credID }
func (f *fakeSession) Mode() string           { return f.mode }
func (f *fakeSession) StartedAt() time.Time   { return time.Now() }
func (f *fakeSession) Reach() string          { return f.reach }
func (f *fakeSession) Stop() error            { return nil }
func (f *fakeSession) Done() <-chan struct{}  { return f.done }
func (f *fakeSession) Err() error             { return nil }
func (f *fakeSession) Pool() share.PoolReader { return nil }
