package claude

import (
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestActive_NoEntry(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestActive_BlobWithMarker(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	setActiveBlob(t, "the-id", store.OAuthTokens{AccessToken: "a", ExpiresAt: 1})
	id, ok := Active()
	if !ok || id != "the-id" {
		t.Errorf("Active() = (%q, %v), want (\"the-id\", true)", id, ok)
	}
}

func TestActive_BlobWithoutMarker(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := (fileBackend{}).Write([]byte(`{"claudeAiOauth":{"accessToken":"x","expiresAt":1}}`)); err != nil {
		t.Fatal(err)
	}
	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() with marker-less blob = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestActive_CorruptBlob(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if err := (fileBackend{}).Write([]byte(`{not json`)); err != nil {
		t.Fatal(err)
	}
	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() with corrupt blob = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestActive_BackendReadError(t *testing.T) {
	withBackend(t, &fakeBackend{ReadErr: errSentinel("backend down")})
	id, ok := Active()
	if ok || id != "" {
		t.Errorf("Active() on backend error = (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestReadActiveBlob_RoundTrip(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()

	if _, ok, err := ReadActiveBlob(); ok || err != nil {
		t.Errorf("ReadActiveBlob fresh = (ok=%v err=%v), want (false, nil)", ok, err)
	}
	setActiveBlob(t, "x", store.OAuthTokens{AccessToken: "y", ExpiresAt: 1})
	blob, ok, err := ReadActiveBlob()
	if err != nil || !ok || len(blob) == 0 {
		t.Errorf("ReadActiveBlob after write: ok=%v err=%v len(blob)=%d", ok, err, len(blob))
	}
}

func TestIsActive_TrueAndFalse(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	setActiveBlob(t, "alpha", store.OAuthTokens{AccessToken: "a", ExpiresAt: 1})
	if !IsActive("alpha") {
		t.Error("IsActive(alpha) = false")
	}
	if IsActive("beta") {
		t.Error("IsActive(beta) = true")
	}
}

func TestActiveID_AndIsManaged(t *testing.T) {
	_, cleanup := setupFakeHome(t)
	defer cleanup()
	if ActiveID() != "" || IsManaged() {
		t.Error("fresh state: expected empty ActiveID and !IsManaged")
	}
	setActiveBlob(t, "x", store.OAuthTokens{AccessToken: "a", ExpiresAt: 1})
	if ActiveID() != "x" {
		t.Errorf("ActiveID = %q", ActiveID())
	}
	if !IsManaged() {
		t.Error("IsManaged after set = false")
	}
}

// errSentinel is a tiny error type so test files can produce typed
// errors for the WriteErr/ReadErr/RemoveErr fakes without importing
// errors.New everywhere.
type errSentinel string

func (e errSentinel) Error() string { return string(e) }
