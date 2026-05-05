package share

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

func TestPromoteResetsCounterOfHealthyOldActivated(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	p.entries["a"].consecutiveFail = 1 // healthy (< 2)
	p.Promote("b", nil)
	if got := p.entries["a"].status; got != statusCandidate {
		t.Errorf("old activated status = %v, want candidate", got)
	}
	if got := p.entries["a"].consecutiveFail; got != 0 {
		t.Errorf("old activated consecutiveFail = %d, want 0 (reset on rotation)", got)
	}
	if got := p.entries["b"].status; got != statusActivated {
		t.Errorf("new activated status = %v, want activated", got)
	}
	if got := p.entries["b"].consecutiveFail; got != 0 {
		t.Errorf("new activated consecutiveFail = %d, want 0", got)
	}
	if got := p.activated; got != "b" {
		t.Errorf("pool.activated = %q, want b", got)
	}
}

func TestPromoteDemotesUnhealthyOldActivatedToDegraded(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	p.entries["a"].consecutiveFail = 3 // unhealthy
	p.Promote("b", nil)
	if got := p.entries["a"].status; got != statusDegraded {
		t.Errorf("old activated status = %v, want degraded", got)
	}
	if got := p.entries["a"].consecutiveFail; got != 3 {
		t.Errorf("old activated consecutiveFail = %d, want 3 (preserved)", got)
	}
}

func TestDemoteSetsActivatedEmpty(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusDegraded, &fakeTokenSource{}),
	})
	p.entries["a"].consecutiveFail = 5
	p.Demote("a")
	if p.activated != "" {
		t.Errorf("activated = %q, want empty", p.activated)
	}
	if got := p.entries["a"].status; got != statusDegraded {
		t.Errorf("status = %v, want degraded", got)
	}
}

func TestDemotePanicsOnSingleton(t *testing.T) {
	p := makePool("a", true, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Demote on singleton pool did not panic")
		}
	}()
	p.Demote("a")
}

func TestSignalActivatedFailedIncrementsCounter(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
	})
	p.SignalActivatedFailed()
	if got := p.entries["a"].consecutiveFail; got != 1 {
		t.Errorf("consecutiveFail = %d, want 1", got)
	}
	p.SignalActivatedFailed()
	if got := p.entries["a"].consecutiveFail; got != 2 {
		t.Errorf("consecutiveFail = %d, want 2", got)
	}
}

func TestSignalActivatedFailedNoOpWhenEmpty(t *testing.T) {
	p := makePool("", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusDegraded, &fakeTokenSource{}),
	})
	p.SignalActivatedFailed()
	if got := p.entries["a"].consecutiveFail; got != 0 {
		t.Errorf("consecutiveFail bumped on empty pool: %d", got)
	}
}

// Drives the !ok branch — activated points to a missing key.
func TestSignalActivatedFailedActivatedMissingFromMap(t *testing.T) {
	p := makePool("ghost", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusCandidate, &fakeTokenSource{}),
	})
	// Should not panic.
	p.SignalActivatedFailed()
}

func TestSignalActivatedFailedRacePromote(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	const N = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			p.SignalActivatedFailed()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			if i%2 == 0 {
				p.Promote("b", nil)
			} else {
				p.Promote("a", nil)
			}
		}
	}()
	wg.Wait()
	// Pool must be in a consistent state — exactly one entry has
	// status activated, and the activated string matches.
	var activatedCount int
	for id, e := range p.entries {
		if e.status == statusActivated {
			activatedCount++
			if id != p.activated {
				t.Errorf("activated map key %q != entry status owner", id)
			}
		}
	}
	if activatedCount != 1 {
		t.Errorf("activatedCount = %d, want 1", activatedCount)
	}
}

func TestEntryStatusString(t *testing.T) {
	cases := []struct {
		s    entryStatus
		want string
	}{
		{statusCandidate, "candidate"},
		{statusActivated, "activated"},
		{statusDegraded, "degraded"},
		{entryStatus(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("status %d.String() = %q, want %q", int(c.s), got, c.want)
		}
	}
}

func TestSnapshotLines(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "", statusDegraded, &fakeTokenSource{}), // no name
	})
	p.entries["a"].lastUsageAt = time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	p.entries["a"].lastFeasibility = 1.5
	p.entries["a"].consecutiveFail = 3
	lines := p.SnapshotLines()
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	combined := lines[0] + "\n" + lines[1]
	if !strings.Contains(combined, "alice") {
		t.Errorf("missing alice: %s", combined)
	}
	if !strings.Contains(combined, "never") {
		t.Errorf("missing 'never' for b's lastUsageAt: %s", combined)
	}
}

func TestPromoteStoresHeaders(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	hdrs := http.Header{"User-Agent": []string{"bob-ua"}, "X-Test": []string{"42"}}
	p.Promote("b", hdrs)
	if got := p.entries["b"].captured.Get("User-Agent"); got != "bob-ua" {
		t.Errorf("captured User-Agent = %q, want bob-ua", got)
	}
	if got := p.entries["b"].captured.Get("X-Test"); got != "42" {
		t.Errorf("captured X-Test = %q, want 42", got)
	}
}

func TestPromoteClonesHeadersOnStore(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	hdrs := http.Header{"X-Test": []string{"original"}}
	p.Promote("b", hdrs)

	// Mutate the source map after Promote.
	hdrs.Set("X-Test", "mutated")

	if got := p.entries["b"].captured.Get("X-Test"); got != "original" {
		t.Errorf("captured X-Test = %q, want original (Promote must clone)", got)
	}
}

func TestActivatedHeadersReturnsActivatedEntry(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
	})
	p.entries["a"].captured = http.Header{"User-Agent": []string{"alice-ua"}}
	got := p.activatedHeaders()
	if got.Get("User-Agent") != "alice-ua" {
		t.Errorf("activatedHeaders().Get(User-Agent) = %q, want alice-ua", got.Get("User-Agent"))
	}
}

func TestActivatedHeadersReturnsNilWhenNoActivated(t *testing.T) {
	p := makePool("", false, map[string]*poolEntry{})
	if got := p.activatedHeaders(); got != nil {
		t.Errorf("activatedHeaders() = %v, want nil", got)
	}
}

func TestActivatedHeadersReturnsNilWhenActivatedMissing(t *testing.T) {
	// activated points to an ID that doesn't exist in entries.
	p := makePool("ghost", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusCandidate, &fakeTokenSource{}),
	})
	if got := p.activatedHeaders(); got != nil {
		t.Errorf("activatedHeaders() = %v, want nil (activated missing from map)", got)
	}
}

func TestSnapshotLinesIncludesCapturedHeadersCount(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	p.entries["a"].captured = http.Header{
		"User-Agent":     []string{"x"},
		"Anthropic-Beta": []string{"y"},
	}
	// b has no captured headers.

	lines := p.SnapshotLines()
	var aLine, bLine string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "alice"):
			aLine = l
		case strings.Contains(l, "bob"):
			bLine = l
		}
	}
	if !strings.Contains(aLine, "headers=2") {
		t.Errorf("alice line %q missing headers=2", aLine)
	}
	if !strings.Contains(bLine, "headers=unset") {
		t.Errorf("bob line %q missing headers=unset", bLine)
	}
}

func TestSnapshotLinesSuppressesHeadersColumnInLaunchMode(t *testing.T) {
	// All entries with captured == nil → column omitted entirely.
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	// p.entries[*].captured all nil by default

	lines := p.SnapshotLines()
	for _, l := range lines {
		if strings.Contains(l, "headers=") {
			t.Errorf("line %q contains headers= column despite no entries having captured headers", l)
		}
	}
}

func TestSnapshotLinesShowsHeadersColumnWhenAnyCaptureSet(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
		"b": newEntry("b", "bob", statusCandidate, &fakeTokenSource{}),
	})
	p.entries["a"].captured = http.Header{"X": []string{"y"}}
	// b.captured stays nil

	lines := p.SnapshotLines()
	var aLine, bLine string
	for _, l := range lines {
		if strings.Contains(l, "alice") {
			aLine = l
		}
		if strings.Contains(l, "bob") {
			bLine = l
		}
	}
	if !strings.Contains(aLine, "headers=1") {
		t.Errorf("alice line %q missing headers=1", aLine)
	}
	if !strings.Contains(bLine, "headers=unset") {
		t.Errorf("bob line %q missing headers=unset", bLine)
	}
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	p := makePool("a", false, map[string]*poolEntry{
		"a": newEntry("a", "alice", statusActivated, &fakeTokenSource{}),
	})
	p.entries["a"].consecutiveFail = 7
	p.entries["a"].lastFeasibility = 1.5
	snap := p.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("len(snap) = %d, want 1", len(snap))
	}
	if snap[0].consecutiveFail != 7 || snap[0].lastFeasibility != 1.5 {
		t.Errorf("snapshot lost fields: %+v", snap[0])
	}
	// Mutate the snapshot, then assert the pool is untouched.
	snap[0].consecutiveFail = 999
	if got := p.entries["a"].consecutiveFail; got != 7 {
		t.Errorf("Snapshot leaked aliasing — pool counter = %d, want 7", got)
	}
}
