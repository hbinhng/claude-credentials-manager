package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setHome overrides HOME so that os.UserHomeDir() (and therefore Dir())
// returns a temporary directory. It returns a cleanup function that
// restores the original value.
func setHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp) // Windows compat
	return tmp
}

// makeCred is a helper that builds a Credential with the given expiry.
func makeCred(id, name string, expiresAt int64) *Credential {
	return &Credential{
		ID:   id,
		Name: name,
		ClaudeAiOauth: OAuthTokens{
			AccessToken:  "at-" + id,
			RefreshToken: "rt-" + id,
			ExpiresAt:    expiresAt,
			Scopes:       []string{"openid", "profile"},
		},
		CreatedAt:       "2025-01-01T00:00:00Z",
		LastRefreshedAt: "2025-01-01T01:00:00Z",
	}
}

// ---------------------------------------------------------------------------
// credential.go
// ---------------------------------------------------------------------------

func TestIsExpired(t *testing.T) {
	t.Parallel()
	now := time.Now().UnixMilli()

	tests := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{"already expired 1 hour ago", now - 3600*1000, true},
		{"expires right now (boundary)", now, true},
		{"not expired, 1 hour left", now + 3600*1000, false},
		{"not expired, 10 seconds left", now + 10*1000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := makeCred("id", "n", tt.expiresAt)
			if got := c.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v (expiresAt=%d, now≈%d)", got, tt.want, tt.expiresAt, now)
			}
		})
	}
}

func TestIsExpiringSoon(t *testing.T) {
	t.Parallel()
	now := time.Now().UnixMilli()

	tests := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{"already expired", now - 1000, false},
		{"expires in 1 minute — within 5 min", now + 60*1000, true},
		{"expires in 4m59s — within 5 min", now + (4*60+59)*1000, true},
		{"expires in 6 min — outside 5 min window", now + 6*60*1000, false},
		{"expires in 10 minutes — outside 5 min", now + 10*60*1000, false},
		{"expires in 10 seconds — within 5 min", now + 10*1000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := makeCred("id", "n", tt.expiresAt)
			if got := c.IsExpiringSoon(); got != tt.want {
				t.Errorf("IsExpiringSoon() = %v, want %v (expiresAt=%d, now≈%d)", got, tt.want, tt.expiresAt, now)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	now := time.Now().UnixMilli()

	tests := []struct {
		name      string
		expiresAt int64
		want      string
	}{
		{"expired", now - 60*1000, "expired"},
		{"expiring soon (2 min left)", now + 2*60*1000, "expiring soon"},
		{"valid (1 hour left)", now + 3600*1000, "valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := makeCred("id", "n", tt.expiresAt)
			if got := c.Status(); got != tt.want {
				t.Errorf("Status() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpiresIn(t *testing.T) {
	t.Parallel()
	now := time.Now().UnixMilli()

	// For future timestamps, add a 500ms buffer so that by the time
	// ExpiresIn() calls time.Now() (a few ms later), integer division
	// still lands in the expected bucket.
	const buf int64 = 500

	tests := []struct {
		name      string
		expiresAt int64
		want      string
	}{
		// Past (expired) — past values are stable because the elapsed
		// time only grows, keeping them in the same bucket.
		{"expired 10 seconds ago — just now", now - 10*1000, "just now"},
		{"expired 59 seconds ago — just now", now - 59*1000, "just now"},
		{"expired 5 minutes ago", now - 5*60*1000, "5 mins ago"},
		{"expired 1 minute ago", now - 60*1000, "1 min ago"},
		{"expired 2 hours ago", now - 2*3600*1000, "2 hrs ago"},
		{"expired 1 hour ago", now - 3600*1000, "1 hr ago"},
		// Future (valid)
		{"expires in 30 seconds", now + 30*1000 + buf, "in 30 secs"},
		{"expires in 1 second", now + 1*1000 + buf, "in 1 sec"},
		{"expires in 5 minutes", now + 5*60*1000 + buf, "in 5 mins"},
		{"expires in 1 minute", now + 60*1000 + buf, "in 1 min"},
		{"expires in 2 hours", now + 2*3600*1000 + buf, "in 2 hrs"},
		{"expires in 1 hour", now + 3600*1000 + buf, "in 1 hr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := makeCred("id", "n", tt.expiresAt)
			got := c.ExpiresIn()
			if got != tt.want {
				t.Errorf("ExpiresIn() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatUnit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		val  int64
		unit string
		want string
	}{
		{1, "min", "1 min"},
		{2, "min", "2 mins"},
		{0, "sec", "0 secs"},
		{1, "hr", "1 hr"},
		{10, "hr", "10 hrs"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := formatUnit(tt.val, tt.unit); got != tt.want {
				t.Errorf("formatUnit(%d, %q) = %q, want %q", tt.val, tt.unit, got, tt.want)
			}
		})
	}
}

func TestIntToStr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		v    int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{123456789, "123456789"},
		{-1, "-1"},
		{-999, "-999"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := intToStr(tt.v); got != tt.want {
				t.Errorf("intToStr(%d) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// store.go
// ---------------------------------------------------------------------------

func TestSaveAndLoad(t *testing.T) {
	home := setHome(t)

	cred := makeCred("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "my-cred", time.Now().Add(time.Hour).UnixMilli())

	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(cred.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify all fields round-trip.
	if got.ID != cred.ID {
		t.Errorf("ID = %q, want %q", got.ID, cred.ID)
	}
	if got.Name != cred.Name {
		t.Errorf("Name = %q, want %q", got.Name, cred.Name)
	}
	if got.ClaudeAiOauth.AccessToken != cred.ClaudeAiOauth.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.ClaudeAiOauth.AccessToken, cred.ClaudeAiOauth.AccessToken)
	}
	if got.ClaudeAiOauth.RefreshToken != cred.ClaudeAiOauth.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.ClaudeAiOauth.RefreshToken, cred.ClaudeAiOauth.RefreshToken)
	}
	if got.ClaudeAiOauth.ExpiresAt != cred.ClaudeAiOauth.ExpiresAt {
		t.Errorf("ExpiresAt = %d, want %d", got.ClaudeAiOauth.ExpiresAt, cred.ClaudeAiOauth.ExpiresAt)
	}
	if len(got.ClaudeAiOauth.Scopes) != len(cred.ClaudeAiOauth.Scopes) {
		t.Errorf("Scopes len = %d, want %d", len(got.ClaudeAiOauth.Scopes), len(cred.ClaudeAiOauth.Scopes))
	}
	if got.CreatedAt != cred.CreatedAt {
		t.Errorf("CreatedAt = %q, want %q", got.CreatedAt, cred.CreatedAt)
	}
	if got.LastRefreshedAt != cred.LastRefreshedAt {
		t.Errorf("LastRefreshedAt = %q, want %q", got.LastRefreshedAt, cred.LastRefreshedAt)
	}

	// Verify the data dir was created inside our temp HOME.
	ccmDir := filepath.Join(home, ".ccm")
	info, err := os.Stat(ccmDir)
	if err != nil {
		t.Fatalf("stat .ccm dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf(".ccm is not a directory")
	}
}

func TestSaveCreatesDirIfMissing(t *testing.T) {
	home := setHome(t)
	ccmDir := filepath.Join(home, ".ccm")

	// Confirm the directory does not exist yet.
	if _, err := os.Stat(ccmDir); !os.IsNotExist(err) {
		t.Fatalf(".ccm should not exist yet, got err=%v", err)
	}

	cred := makeCred("11111111-1111-1111-1111-111111111111", "test", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(ccmDir)
	if err != nil {
		t.Fatalf("after Save, .ccm dir missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatal(".ccm is not a directory")
	}
}

func TestFilePermissions(t *testing.T) {
	setHome(t)

	cred := makeCred("22222222-2222-2222-2222-222222222222", "perm-test", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(CredPath(cred.ID))
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permission = %04o, want 0600", perm)
	}
}

func TestAtomicWriteNoTempFileLeftBehind(t *testing.T) {
	home := setHome(t)

	cred := makeCred("33333333-3333-3333-3333-333333333333", "atomic-test", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ccmDir := filepath.Join(home, ".ccm")
	entries, err := os.ReadDir(ccmDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestDelete(t *testing.T) {
	setHome(t)

	cred := makeCred("44444444-4444-4444-4444-444444444444", "del-test", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Delete(cred.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(CredPath(cred.ID)); !os.IsNotExist(err) {
		t.Errorf("credential file should be gone, got err=%v", err)
	}
}

func TestDeleteNonExistent(t *testing.T) {
	setHome(t)

	// Ensure directory exists so we don't error on missing dir.
	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	err := Delete("nonexistent-id")
	if err == nil {
		t.Fatal("Delete of non-existent credential should return an error")
	}
}

func TestListEmpty(t *testing.T) {
	setHome(t)

	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	creds, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials, got %d", len(creds))
	}
}

func TestListOne(t *testing.T) {
	setHome(t)

	cred := makeCred("55555555-5555-5555-5555-555555555555", "single", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	creds, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(creds))
	}
	if creds[0].ID != cred.ID {
		t.Errorf("ID = %q, want %q", creds[0].ID, cred.ID)
	}
}

func TestListMultiple(t *testing.T) {
	setHome(t)

	ids := []string{
		"66666666-6666-6666-6666-666666666666",
		"77777777-7777-7777-7777-777777777777",
		"88888888-8888-8888-8888-888888888888",
	}
	for i, id := range ids {
		c := makeCred(id, "cred-"+intToStr(int64(i)), time.Now().Add(time.Hour).UnixMilli())
		if err := Save(c); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}

	creds, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 3 {
		t.Fatalf("expected 3 credentials, got %d", len(creds))
	}
}

func TestListSkipsCorruptJSON(t *testing.T) {
	home := setHome(t)

	// Save one valid credential.
	valid := makeCred("99999999-9999-9999-9999-999999999999", "valid", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(valid); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Write a corrupt JSON file that matches the glob pattern.
	corrupt := filepath.Join(home, ".ccm", "corrupt-id.credentials.json")
	if err := os.WriteFile(corrupt, []byte("{bad json!!!"), 0600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	creds, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential (skip corrupt), got %d", len(creds))
	}
	if creds[0].ID != valid.ID {
		t.Errorf("ID = %q, want %q", creds[0].ID, valid.ID)
	}
}

func TestListSkipsTmpSuffixFiles(t *testing.T) {
	home := setHome(t)

	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	// A file whose ID portion ends in ".tmp" should be skipped by the
	// HasSuffix(id, ".tmp") guard in List.
	data, _ := json.MarshalIndent(makeCred("leftover.tmp", "tmp-cred", time.Now().Add(time.Hour).UnixMilli()), "", "  ")
	if err := os.WriteFile(filepath.Join(home, ".ccm", "leftover.tmp.credentials.json"), data, 0600); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	creds, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials (skip .tmp), got %d", len(creds))
	}
}

func TestLoadNonExistent(t *testing.T) {
	setHome(t)

	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	_, err := Load("does-not-exist")
	if err == nil {
		t.Fatal("Load of non-existent id should return an error")
	}
}

// ---------------------------------------------------------------------------
// resolve.go
// ---------------------------------------------------------------------------

func TestResolveExactUUID(t *testing.T) {
	setHome(t)

	id := "aabbccdd-1111-2222-3333-444444444444"
	cred := makeCred(id, "exact-uuid", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Resolve(id)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
}

func TestResolveExactName(t *testing.T) {
	setHome(t)

	cred := makeCred("bbccddee-1111-2222-3333-444444444444", "my-profile", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Resolve("my-profile")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "my-profile" {
		t.Errorf("Name = %q, want %q", got.Name, "my-profile")
	}
}

func TestResolveUUIDPrefix(t *testing.T) {
	setHome(t)

	cred := makeCred("abcd1234-aaaa-bbbb-cccc-dddddddddddd", "prefix-test", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 4-char prefix should match.
	got, err := Resolve("abcd")
	if err != nil {
		t.Fatalf("Resolve(4 chars): %v", err)
	}
	if got.ID != cred.ID {
		t.Errorf("ID = %q, want %q", got.ID, cred.ID)
	}

	// 8-char prefix should also match.
	got, err = Resolve("abcd1234")
	if err != nil {
		t.Fatalf("Resolve(8 chars): %v", err)
	}
	if got.ID != cred.ID {
		t.Errorf("ID = %q, want %q", got.ID, cred.ID)
	}
}

func TestResolveAmbiguousPrefix(t *testing.T) {
	setHome(t)

	// Two credentials that share the same 4-char prefix.
	c1 := makeCred("abcd1111-aaaa-bbbb-cccc-dddddddddddd", "first", time.Now().Add(time.Hour).UnixMilli())
	c2 := makeCred("abcd2222-aaaa-bbbb-cccc-dddddddddddd", "second", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(c1); err != nil {
		t.Fatalf("Save c1: %v", err)
	}
	if err := Save(c2); err != nil {
		t.Fatalf("Save c2: %v", err)
	}

	_, err := Resolve("abcd")
	if err == nil {
		t.Fatal("expected ambiguous error, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention 'ambiguous', got: %v", err)
	}
	if !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Errorf("error should list both candidates, got: %v", err)
	}
}

func TestResolveShortPrefixNoMatch(t *testing.T) {
	setHome(t)

	cred := makeCred("xyz99999-aaaa-bbbb-cccc-dddddddddddd", "short-test", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 3-char prefix is too short for prefix matching — should not match.
	_, err := Resolve("xyz")
	if err == nil {
		t.Fatal("expected error for 3-char prefix, got nil")
	}
	if !strings.Contains(err.Error(), "no credential matching") {
		t.Errorf("error = %q, want 'no credential matching'", err.Error())
	}
}

func TestResolveNoMatch(t *testing.T) {
	setHome(t)

	cred := makeCred("deadbeef-aaaa-bbbb-cccc-dddddddddddd", "some-cred", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := Resolve("totally-different")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no credential matching") {
		t.Errorf("error = %q, want 'no credential matching'", err.Error())
	}
}

func TestResolveEmptyStore(t *testing.T) {
	setHome(t)

	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	_, err := Resolve("anything")
	if err == nil {
		t.Fatal("expected error for empty store, got nil")
	}
	if !strings.Contains(err.Error(), "no credentials found") {
		t.Errorf("error = %q, want 'no credentials found'", err.Error())
	}
}

func TestResolveNameTakesPriorityOverPrefix(t *testing.T) {
	setHome(t)

	// A credential whose name happens to be a valid 4+ char string that is
	// also a prefix of another credential's UUID.
	c1 := makeCred("alpha111-aaaa-bbbb-cccc-dddddddddddd", "alpha111", time.Now().Add(time.Hour).UnixMilli())
	c2 := makeCred("alpha222-aaaa-bbbb-cccc-dddddddddddd", "other", time.Now().Add(time.Hour).UnixMilli())
	if err := Save(c1); err != nil {
		t.Fatalf("Save c1: %v", err)
	}
	if err := Save(c2); err != nil {
		t.Fatalf("Save c2: %v", err)
	}

	// "alpha111" is an exact name match on c1, and also a UUID prefix
	// matching both. Exact name match should win.
	got, err := Resolve("alpha111")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != c1.ID {
		t.Errorf("expected exact name match (c1), got ID=%q", got.ID)
	}
}

// TestOAuthTokens_RoundTrip_PreservesUnknownFields verifies that fields
// Claude Code adds beyond ccm's typed surface (rateLimitTier,
// subscriptionType, future additions) survive a JSON unmarshal/marshal
// cycle. Without the unknown-field passthrough this test would fail —
// see the smoke test in commit history.
func TestOAuthTokens_RoundTrip_PreservesUnknownFields(t *testing.T) {
	original := []byte(`{
		"accessToken": "tok",
		"refreshToken": "ref",
		"expiresAt": 12345,
		"scopes": ["a", "b"],
		"rateLimitTier": "max20x",
		"subscriptionType": "pro",
		"futureField": {"nested": true}
	}`)

	var tok OAuthTokens
	if err := json.Unmarshal(original, &tok); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tok.AccessToken != "tok" || tok.RefreshToken != "ref" || tok.ExpiresAt != 12345 {
		t.Errorf("typed fields wrong: %+v", tok)
	}
	if len(tok.Scopes) != 2 || tok.Scopes[0] != "a" {
		t.Errorf("scopes: %v", tok.Scopes)
	}

	out, err := json.Marshal(&tok)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Round-trip the output through a generic map and compare keys +
	// values to the original, modulo whitespace.
	var got, want map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatal(err)
	}

	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q missing after round-trip", k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("key %q added after round-trip", k)
		}
	}
}

// TestOAuthTokens_NoExtras emits a normal blob without unknown fields
// to confirm marshal output stays clean (no nil-extras pollution).
func TestOAuthTokens_NoExtras(t *testing.T) {
	tok := OAuthTokens{
		AccessToken:  "a",
		RefreshToken: "r",
		ExpiresAt:    1,
		Scopes:       []string{"s"},
	}
	out, err := json.Marshal(&tok)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 {
		t.Errorf("unexpected key count: %d, want 4 (got %v)", len(m), m)
	}
}

// TestOAuthTokens_MalformedTypedField propagates the parse error
// rather than silently zeroing the field.
func TestOAuthTokens_MalformedTypedField(t *testing.T) {
	bad := []byte(`{"accessToken": 42, "refreshToken": "r", "expiresAt": 1, "scopes": ["s"]}`)
	var tok OAuthTokens
	if err := json.Unmarshal(bad, &tok); err == nil {
		t.Error("expected error on type mismatch, got nil")
	}
}

// TestOAuthTokens_UnmarshalGarbage rejects non-JSON input.
func TestOAuthTokens_UnmarshalGarbage(t *testing.T) {
	var tok OAuthTokens
	if err := json.Unmarshal([]byte("not json"), &tok); err == nil {
		t.Error("expected error on garbage input")
	}
}
