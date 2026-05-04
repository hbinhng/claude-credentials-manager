package share

import (
	"errors"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// fakeTokenSource is a mock for credState.Fresh — it lets pool tests
// avoid the OAuth/flock machinery entirely.
type fakeTokenSource struct {
	token string
	err   error
	calls int
}

func (f *fakeTokenSource) Fresh() (string, error) {
	f.calls++
	return f.token, f.err
}

// makePool builds a pool with the supplied entries (each
// (id, status, tokenSource)) and the activated id, for tests.
func makePool(activated string, singleton bool, entries map[string]*poolEntry) *credPool {
	return &credPool{
		entries:   entries,
		activated: activated,
		singleton: singleton,
	}
}

func newEntry(id, name string, status entryStatus, tok tokenSource) *poolEntry {
	return &poolEntry{
		state:  &credStateAdapter{id: id, name: name, src: tok},
		status: status,
	}
}

// credStateAdapter lets fakeTokenSource pretend to be a *credState
// for pool-only tests that never invoke real refresh logic. It
// stores enough metadata that the pool can render log lines.
type credStateAdapter struct {
	id   string
	name string
	src  tokenSource
}

func (c *credStateAdapter) Fresh() (string, error)     { return c.src.Fresh() }
func (c *credStateAdapter) credID() string             { return c.id }
func (c *credStateAdapter) credName() string           { return c.name }
func (c *credStateAdapter) credExpiresAt() time.Time   { return time.Time{} }
func (c *credStateAdapter) credPtr() *store.Credential { return nil }

// suppress unused warnings: oauth is imported here so future tests in this
// file can refer to oauth.* without re-importing.
var _ = oauth.UsageInfo{}

func TestPoolFreshRoutesToActivated(t *testing.T) {
	tA := &fakeTokenSource{token: "tokA"}
	tB := &fakeTokenSource{token: "tokB"}
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, tA),
		"b": newEntry("b", "bob", statusCandidate, tB),
	})
	got, err := p.Fresh()
	if err != nil {
		t.Fatalf("Fresh err: %v", err)
	}
	if got != "tokA" {
		t.Errorf("got %q, want tokA", got)
	}
	if tB.calls != 0 {
		t.Errorf("non-activated source was called %d times", tB.calls)
	}
}

func TestPoolFreshNoActivated(t *testing.T) {
	p := makePool("", false, map[string]*poolEntry{})
	_, err := p.Fresh()
	if !errors.Is(err, errNoActivated) {
		t.Errorf("got err %v, want errNoActivated", err)
	}
}
