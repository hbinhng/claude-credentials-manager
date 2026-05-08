package middleware_test

import (
	"sync"
	"testing"

	codexmw "github.com/hbinhng/claude-credentials-manager/internal/codex/middleware"
)

// TestUsageTee_RecordAndSnapshot verifies basic record + ordered snapshot.
func TestUsageTee_RecordAndSnapshot(t *testing.T) {
	tee := codexmw.NewUsageTee(5)

	tee.Record(codexmw.UsageEvent{InputTokens: 10, OutputTokens: 5})
	tee.Record(codexmw.UsageEvent{InputTokens: 20, OutputTokens: 10})
	tee.Record(codexmw.UsageEvent{InputTokens: 30, OutputTokens: 15, CacheReadInputTokens: 3})

	snap := tee.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	// Oldest first.
	if snap[0].InputTokens != 10 {
		t.Errorf("snap[0].InputTokens = %d, want 10", snap[0].InputTokens)
	}
	if snap[2].CacheReadInputTokens != 3 {
		t.Errorf("snap[2].CacheReadInputTokens = %d, want 3", snap[2].CacheReadInputTokens)
	}
}

// TestUsageTee_EmptySnapshot verifies that an untouched tee returns an empty
// slice (not nil, but length zero).
func TestUsageTee_EmptySnapshot(t *testing.T) {
	tee := codexmw.NewUsageTee(4)
	snap := tee.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("empty snapshot len = %d, want 0", len(snap))
	}
}

// TestUsageTee_RingBufferOverflow verifies that recording more events than
// capacity evicts the oldest and keeps the most recent cap events.
func TestUsageTee_RingBufferOverflow(t *testing.T) {
	const cap = 3
	tee := codexmw.NewUsageTee(cap)

	// Record 5 events; the ring should retain the last 3 (events 3, 4, 5).
	for i := 1; i <= 5; i++ {
		tee.Record(codexmw.UsageEvent{InputTokens: i * 10})
	}

	snap := tee.Snapshot()
	if len(snap) != cap {
		t.Fatalf("snapshot len = %d, want %d", len(snap), cap)
	}
	// Oldest-first after wrap: events 3, 4, 5.
	want := []int{30, 40, 50}
	for i, ev := range snap {
		if ev.InputTokens != want[i] {
			t.Errorf("snap[%d].InputTokens = %d, want %d", i, ev.InputTokens, want[i])
		}
	}
}

// TestUsageTee_RingBufferExactCapacity verifies snapshot order when
// exactly cap events have been recorded (no overflow yet).
func TestUsageTee_RingBufferExactCapacity(t *testing.T) {
	const cap = 3
	tee := codexmw.NewUsageTee(cap)

	for i := 1; i <= cap; i++ {
		tee.Record(codexmw.UsageEvent{OutputTokens: i})
	}

	snap := tee.Snapshot()
	if len(snap) != cap {
		t.Fatalf("snapshot len = %d, want %d", len(snap), cap)
	}
	for i, ev := range snap {
		if ev.OutputTokens != i+1 {
			t.Errorf("snap[%d].OutputTokens = %d, want %d", i, ev.OutputTokens, i+1)
		}
	}
}

// TestUsageTee_ConcurrentSafe verifies that concurrent Record + Snapshot
// calls do not race (run with -race).
func TestUsageTee_ConcurrentSafe(t *testing.T) {
	tee := codexmw.NewUsageTee(16)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			tee.Record(codexmw.UsageEvent{InputTokens: n})
		}(i)
		go func() {
			defer wg.Done()
			_ = tee.Snapshot()
		}()
	}
	wg.Wait()
}
