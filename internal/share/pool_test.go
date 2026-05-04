package share

import (
	"errors"
	"fmt"
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

func TestMarkProbeCandidateSuccessResetsFailCounter(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	p.entries["b"].consecutiveFail = 1
	p.MarkProbe("b", &oauth.UsageInfo{}, nil)
	if got := p.entries["b"].consecutiveFail; got != 0 {
		t.Errorf("consecutiveFail = %d, want 0", got)
	}
	if got := p.entries["b"].status; got != statusCandidate {
		t.Errorf("status = %v, want candidate", got)
	}
}

func TestMarkProbeCandidateDegradesAfter2Failures(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	p.MarkProbe("b", nil, fmt.Errorf("fail 1"))
	if p.entries["b"].status != statusCandidate {
		t.Errorf("after 1 fail status = %v, want candidate", p.entries["b"].status)
	}
	p.MarkProbe("b", nil, fmt.Errorf("fail 2"))
	if p.entries["b"].status != statusDegraded {
		t.Errorf("after 2 fails status = %v, want degraded", p.entries["b"].status)
	}
}

func TestMarkProbeDegradedRecoversOnFirstSuccess(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"b": newEntry("b", "bob", statusDegraded, &fakeTokenSource{}),
	})
	p.entries["b"].consecutiveFail = 5
	p.MarkProbe("b", &oauth.UsageInfo{}, nil)
	if got := p.entries["b"].status; got != statusCandidate {
		t.Errorf("status = %v, want candidate", got)
	}
	if got := p.entries["b"].consecutiveFail; got != 0 {
		t.Errorf("consecutiveFail = %d, want 0", got)
	}
}

func TestMarkProbeActivatedNotDemoted(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
	})
	p.MarkProbe("a", nil, fmt.Errorf("fail 1"))
	p.MarkProbe("a", nil, fmt.Errorf("fail 2"))
	p.MarkProbe("a", nil, fmt.Errorf("fail 3"))
	if got := p.entries["a"].status; got != statusActivated {
		t.Errorf("status = %v, want activated (MarkProbe must NEVER demote activated)", got)
	}
	if got := p.entries["a"].consecutiveFail; got != 3 {
		t.Errorf("consecutiveFail = %d, want 3", got)
	}
}

func TestMarkProbeUnknownIDNoOp(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
	})
	// Should not panic.
	p.MarkProbe("nonexistent", &oauth.UsageInfo{}, nil)
}

func TestMarkProbeStoresLastUsageOnSuccess(t *testing.T) {
	info := &oauth.UsageInfo{Quotas: []oauth.Quota{{Name: "5h", Used: 42}}}
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
	})
	p.MarkProbe("a", info, nil)
	if p.entries["a"].lastUsage != info {
		t.Errorf("lastUsage not stored")
	}
	if p.entries["a"].lastUsageAt.IsZero() {
		t.Errorf("lastUsageAt not stamped")
	}
}
