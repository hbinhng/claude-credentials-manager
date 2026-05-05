package share

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// withFakeUsage installs a fake oauth.FetchUsageFn for the test.
// Defined in pool_builder_test.go and reused by other _test.go
// files in this package.
func withFakeUsage(t *testing.T, fn func(string) *oauth.UsageInfo) {
	t.Helper()
	orig := oauth.FetchUsageFn
	oauth.FetchUsageFn = fn
	t.Cleanup(func() { oauth.FetchUsageFn = orig })
}

// makeCredWithExpiry is the richer constructor BuildPool tests need.
// (The existing helper `makeCred(t, id, accessToken)` from
// credstate_test.go only takes two strings — too narrow for our
// purposes.)
func makeCredWithExpiry(t *testing.T, id, name string, expiresIn time.Duration) *store.Credential {
	t.Helper()
	return &store.Credential{
		ID:   id,
		Name: name,
		ClaudeAiOauth: store.OAuthTokens{
			AccessToken:  "atk-" + id,
			RefreshToken: "rtk-" + id,
			ExpiresAt:    time.Now().Add(expiresIn).UnixMilli(),
			Scopes:       []string{"user:inference"},
		},
	}
}

// writeCredToFile persists a credential to the fake HOME via
// store.Save and fails the test if it errors.
func writeCredToFile(t *testing.T, c *store.Credential) {
	t.Helper()
	if err := store.Save(c); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
}

// NOTE: setupFakeHome already exists at credstate_test.go:21 with
// the same signature — REUSE it; do NOT redefine here.

// stubCaptureCredOK installs a default captureCredFn that returns a
// minimal canned header set for any cred. Used by BuildPool tests
// that don't care about per-cred header content but need capture
// to succeed (otherwise BuildPool would attempt to spawn `claude`).
func stubCaptureCredOK(t *testing.T) {
	t.Helper()
	orig := captureCredFn
	captureCredFn = func(_ *store.Credential, _ string) (http.Header, error) {
		return http.Header{"User-Agent": []string{"stub"}}, nil
	}
	t.Cleanup(func() { captureCredFn = orig })
}

func TestBuildPoolNoArgsUsesAllValid(t *testing.T) {
	setupFakeHome(t)
	stubCaptureCredOK(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 10, ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 5, ResetsAt: time.Now().Add(24 * time.Hour).Format(time.RFC3339)},
		}}
	})

	pool, initial, err := BuildPool(nil, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if got := len(pool.entries); got != 2 {
		t.Errorf("pool entries = %d, want 2", got)
	}
	if pool.singleton {
		t.Errorf("pool.singleton = true with 2 entries")
	}
	if initial == nil {
		t.Fatal("initial cred is nil")
	}
}

func TestBuildPoolExplicitArgs(t *testing.T) {
	setupFakeHome(t)
	stubCaptureCredOK(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	c := makeCredWithExpiry(t, "33333333-3333-3333-3333-333333333333", "carol", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)
	writeCredToFile(t, c)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{}
	})

	pool, _, err := BuildPool([]string{"alice", "carol"}, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if got := len(pool.entries); got != 2 {
		t.Errorf("pool entries = %d, want 2", got)
	}
	if _, ok := pool.entries["22222222-2222-2222-2222-222222222222"]; ok {
		t.Errorf("bob should not be in pool")
	}
}

func TestBuildPoolDedupesArgs(t *testing.T) {
	setupFakeHome(t)
	stubCaptureCredOK(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	writeCredToFile(t, a)
	withFakeUsage(t, func(token string) *oauth.UsageInfo { return &oauth.UsageInfo{} })

	pool, _, err := BuildPool([]string{"alice", "alice", "11111111"}, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if got := len(pool.entries); got != 1 {
		t.Errorf("pool entries = %d, want 1 (deduped)", got)
	}
	if !pool.singleton {
		t.Errorf("pool.singleton = false, want true (one entry)")
	}
}

func TestBuildPoolUnresolvedArgFatal(t *testing.T) {
	setupFakeHome(t)
	withFakeUsage(t, func(token string) *oauth.UsageInfo { return &oauth.UsageInfo{} })

	_, _, err := BuildPool([]string{"nonexistent"}, "", false)
	if err == nil {
		t.Fatal("BuildPool succeeded with unresolvable arg")
	}
}

func TestBuildPoolUsageProbeRejectsCred(t *testing.T) {
	setupFakeHome(t)
	stubCaptureCredOK(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		if strings.Contains(token, "1111") {
			return &oauth.UsageInfo{Error: "HTTP 403"}
		}
		return &oauth.UsageInfo{}
	})

	pool, _, err := BuildPool(nil, "", false) // implicit pool — partial reject is OK
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if got := len(pool.entries); got != 1 {
		t.Errorf("pool entries = %d, want 1 (alice rejected)", got)
	}
}

func TestBuildPoolAllRejectedFatal(t *testing.T) {
	// When EVERY admitted cred fails probe (regardless of
	// explicit-vs-implicit), BuildPool returns a fatal error. The
	// old explicit-only-fatal rule was relaxed: a partial probe
	// failure on explicit args drops the bad cred but keeps the
	// session alive on the survivors.
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	writeCredToFile(t, a)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Error: "HTTP 403"}
	})

	_, _, err := BuildPool([]string{"alice"}, "", false)
	if err == nil {
		t.Fatal("BuildPool succeeded with zero usable creds")
	}
}

func TestBuildPoolExplicitArgRejectedDropsAndContinues(t *testing.T) {
	// Two explicit creds; one fails the usage probe. The bad cred
	// should be dropped and the session should continue with the
	// good one.
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		// alice fails probe; bob succeeds
		if token == a.ClaudeAiOauth.AccessToken {
			return &oauth.UsageInfo{Error: "HTTP 429"}
		}
		return &oauth.UsageInfo{}
	})

	stubCaptureCredOK(t)
	pool, _, err := BuildPool([]string{"alice", "bob"}, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v (want nil — bad cred should be dropped, not fatal)", err)
	}
	if _, present := pool.entries[a.ID]; present {
		t.Errorf("alice (probe-failed) should not be in pool")
	}
	if _, present := pool.entries[b.ID]; !present {
		t.Errorf("bob (probe-ok) should be in pool")
	}
}

func TestBuildPoolEmptyStoreFatal(t *testing.T) {
	setupFakeHome(t)
	withFakeUsage(t, func(token string) *oauth.UsageInfo { return &oauth.UsageInfo{} })

	_, _, err := BuildPool(nil, "", false)
	if err == nil {
		t.Fatal("BuildPool succeeded with empty store")
	}
}

func TestBuildPoolPicksHighestFeasibilityInitial(t *testing.T) {
	setupFakeHome(t)
	stubCaptureCredOK(t)
	// alice: low quota, distant reset; bob: high quota, short reset
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		if strings.Contains(token, "1111") {
			return &oauth.UsageInfo{Quotas: []oauth.Quota{
				{Name: "5h", Used: 90, ResetsAt: time.Now().Add(4 * time.Hour).Format(time.RFC3339)},
				{Name: "7d", Used: 80, ResetsAt: time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)},
			}}
		}
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 10, ResetsAt: time.Now().Add(30 * time.Minute).Format(time.RFC3339)},
			{Name: "7d", Used: 5, ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
		}}
	})

	pool, initial, err := BuildPool(nil, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if got := initial.ID; got != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("initial = %s, want bob (higher feasibility)", got)
	}
	if pool.activated != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("pool.activated = %q, want bob", pool.activated)
	}
}

// Refresh is exercised via the existing oauth.TokenURL httptest seam.
// We inject a server that responds to the OAuth refresh request so
// BuildPool's Fresh() call doesn't go to the real network.
func setupRefreshStub(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"refreshed","refresh_token":"refreshed-rt","expires_in":3600,"scope":"user:inference"}`))
	}))
	t.Cleanup(srv.Close)
	orig := oauth.TokenURL
	oauth.TokenURL = srv.URL
	t.Cleanup(func() { oauth.TokenURL = orig })
	return &calls
}

func TestBuildPoolImplicitAllRejected(t *testing.T) {
	setupFakeHome(t)
	// Two creds — the singleton path skips the usage probe, so to
	// exercise the "all rejected by probe" rejection we need >1 cred
	// to enter Pass B.
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)
	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Error: "HTTP 403"}
	})

	_, _, err := BuildPool(nil, "", false)
	if err == nil {
		t.Fatal("BuildPool succeeded with implicit pool but every cred rejected")
	}
	if !strings.Contains(err.Error(), "no usable credentials in pool") {
		t.Errorf("err = %v; want 'no usable credentials in pool' message", err)
	}
}

func TestBuildPoolRefreshFailureRejectsCred(t *testing.T) {
	setupFakeHome(t)
	// Set up a cred whose refresh will hit a 401 from the OAuth server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	orig := oauth.TokenURL
	oauth.TokenURL = srv.URL
	t.Cleanup(func() { oauth.TokenURL = orig })

	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", -time.Hour) // expired → forces refresh
	writeCredToFile(t, a)
	withFakeUsage(t, func(token string) *oauth.UsageInfo { return &oauth.UsageInfo{} })

	_, _, err := BuildPool([]string{"alice"}, "", false)
	if err == nil {
		t.Fatal("BuildPool succeeded with refresh-failing cred named explicitly")
	}
	if !strings.Contains(err.Error(), "refresh failed") && !strings.Contains(err.Error(), "rejected") {
		t.Errorf("err = %v; want 'refresh failed' or 'rejected' message", err)
	}
}

func TestCredLogNameFallback(t *testing.T) {
	c1 := &store.Credential{ID: "abcdef0123456789", Name: "alice"}
	if got := credLogName(c1); got != "alice" {
		t.Errorf("got %q, want alice", got)
	}
	c2 := &store.Credential{ID: "abcdef0123456789"}
	if got := credLogName(c2); got != "abcdef01" {
		t.Errorf("got %q, want abcdef01 (shortID fallback)", got)
	}
}

func TestJoinLinesEmpty(t *testing.T) {
	if got := joinLines(nil); got != "" {
		t.Errorf("joinLines(nil) = %q, want empty", got)
	}
	if got := joinLines([]string{"only"}); got != "only" {
		t.Errorf("joinLines single = %q", got)
	}
	if got := joinLines([]string{"a", "b"}); got != "a\n  b" {
		t.Errorf("joinLines multi = %q", got)
	}
}

func TestBuildPoolCapturesInitialActivated(t *testing.T) {
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	writeCredToFile(t, a)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 10, ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 5, ResetsAt: time.Now().Add(24 * time.Hour).Format(time.RFC3339)},
		}}
	})

	// Stub captureCredFn.
	origCapture := captureCredFn
	defer func() { captureCredFn = origCapture }()
	captureCalls := 0
	captureCredFn = func(cred *store.Credential, _ string) (http.Header, error) {
		captureCalls++
		return http.Header{"X-Cred": []string{cred.Name}}, nil
	}

	pool, initialCred, err := BuildPool(nil, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if captureCalls != 1 {
		t.Errorf("captureCalls = %d, want 1", captureCalls)
	}
	if initialCred.ID != a.ID {
		t.Errorf("initial = %s, want alice", initialCred.ID)
	}
	if got := pool.entries[a.ID].captured.Get("X-Cred"); got != "alice" {
		t.Errorf("captured X-Cred = %q, want alice", got)
	}
}

func TestBuildPoolFallsThroughToNextBestOnCaptureFailure(t *testing.T) {
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	// Make alice the higher-feasibility cred.
	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		if strings.Contains(token, "1111") {
			return &oauth.UsageInfo{Quotas: []oauth.Quota{
				{Name: "5h", Used: 10, ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
				{Name: "7d", Used: 5, ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
			}}
		}
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 90, ResetsAt: time.Now().Add(4 * time.Hour).Format(time.RFC3339)},
			{Name: "7d", Used: 80, ResetsAt: time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)},
		}}
	})

	origCapture := captureCredFn
	defer func() { captureCredFn = origCapture }()
	captureCredFn = func(cred *store.Credential, _ string) (http.Header, error) {
		if cred.Name == "alice" {
			return nil, errors.New("alice capture broken")
		}
		return http.Header{"X-Cred": []string{cred.Name}}, nil
	}

	pool, initialCred, err := BuildPool(nil, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if initialCred.ID != b.ID {
		t.Errorf("initial = %s, want bob (alice capture failed)", initialCred.ID)
	}
	if _, present := pool.entries[a.ID]; present {
		t.Errorf("alice should not be in pool entries")
	}
	if got := pool.entries[b.ID].captured.Get("X-Cred"); got != "bob" {
		t.Errorf("captured X-Cred = %q, want bob", got)
	}
}

func TestBuildPoolImplicitAllCapturesFailFatal(t *testing.T) {
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	writeCredToFile(t, a)

	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		return &oauth.UsageInfo{}
	})

	origCapture := captureCredFn
	defer func() { captureCredFn = origCapture }()
	captureCredFn = func(_ *store.Credential, _ string) (http.Header, error) {
		return nil, errors.New("everything broken")
	}

	_, _, err := BuildPool(nil, "", false)
	if err == nil {
		t.Fatal("BuildPool: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no candidate could be captured") &&
		!strings.Contains(err.Error(), "no usable credentials") {
		t.Errorf("error %q unexpected", err)
	}
}

func TestBuildPoolExplicitArgCaptureFailureDropsAndContinues(t *testing.T) {
	// Two explicit creds; one fails capture. The bad cred should
	// be dropped and the session should continue with the other.
	// (Previously was fatal; relaxed for resilience.)
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	withFakeUsage(t, func(token string) *oauth.UsageInfo { return &oauth.UsageInfo{} })

	origCapture := captureCredFn
	defer func() { captureCredFn = origCapture }()
	captureCredFn = func(cred *store.Credential, _ string) (http.Header, error) {
		if cred.Name == "alice" {
			return nil, errors.New("alice capture broken")
		}
		return http.Header{}, nil
	}

	pool, initial, err := BuildPool([]string{"alice", "bob"}, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v (want nil — alice capture failure should drop alice, not abort)", err)
	}
	if initial.Name != "bob" {
		t.Errorf("initial = %s, want bob (alice capture failed)", initial.Name)
	}
	if _, present := pool.entries[a.ID]; present {
		t.Errorf("alice should not be in pool entries (capture failed)")
	}
}

func TestBuildPoolExpiredCredIsRefreshedNotSkipped(t *testing.T) {
	setupFakeHome(t)
	stubCaptureCredOK(t)
	calls := setupRefreshStub(t)

	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", -time.Hour) // expired
	writeCredToFile(t, a)
	withFakeUsage(t, func(token string) *oauth.UsageInfo { return &oauth.UsageInfo{} })

	pool, _, err := BuildPool(nil, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if got := len(pool.entries); got != 1 {
		t.Errorf("pool entries = %d, want 1", got)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
}

func TestBuildPool_Singleton_SkipsUsageProbe(t *testing.T) {
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	writeCredToFile(t, a)

	called := 0
	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		called++
		t.Errorf("oauth.FetchUsageFn was called for singleton pool")
		return &oauth.UsageInfo{}
	})

	stubCaptureCredOK(t)

	pool, initial, err := BuildPool(nil, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if pool == nil || initial == nil {
		t.Fatalf("nil pool/initial: %v / %v", pool, initial)
	}
	if !pool.singleton {
		t.Errorf("pool.singleton = false, want true")
	}
	if called != 0 {
		t.Errorf("FetchUsageFn called %d times for singleton, want 0", called)
	}
	// Lone cred entered with lastUsage=nil per the spec.
	if e := pool.entries[initial.ID]; e == nil {
		t.Fatalf("initial cred not in pool.entries")
	} else if e.lastUsage != nil {
		t.Errorf("singleton lastUsage = %+v, want nil (probe skipped)", e.lastUsage)
	}
}

func TestBuildPool_MultiCred_StillProbes(t *testing.T) {
	setupFakeHome(t)
	a := makeCredWithExpiry(t, "11111111-1111-1111-1111-111111111111", "alice", 6*time.Hour)
	b := makeCredWithExpiry(t, "22222222-2222-2222-2222-222222222222", "bob", 6*time.Hour)
	writeCredToFile(t, a)
	writeCredToFile(t, b)

	called := 0
	withFakeUsage(t, func(token string) *oauth.UsageInfo {
		called++
		return &oauth.UsageInfo{Quotas: []oauth.Quota{
			{Name: "5h", Used: 5},
			{Name: "7d", Used: 5},
		}}
	})

	stubCaptureCredOK(t)

	pool, _, err := BuildPool(nil, "", false)
	if err != nil {
		t.Fatalf("BuildPool: %v", err)
	}
	if pool.singleton {
		t.Errorf("pool.singleton = true, want false (multi-cred)")
	}
	if called != 2 {
		t.Errorf("FetchUsageFn called %d times, want 2 (one per cred)", called)
	}
}
