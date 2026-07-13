package grokoauth

import (
	"encoding/base64"
	"errors"
	"testing"
)

// TestParseCallbackInput covers the http-prefix detection that lets the
// user paste either the full redirect URL or the bare authorization code.
func TestParseCallbackInput(t *testing.T) {
	// Full redirect URL → parsed as URL, code+state extracted, wasURL=true.
	code, state, wasURL, err := parseCallbackInput("http://127.0.0.1:56121/callback?code=C&state=S")
	if err != nil || !wasURL || code != "C" || state != "S" {
		t.Fatalf("URL: got code=%q state=%q wasURL=%v err=%v", code, state, wasURL, err)
	}

	// https prefix is also treated as a URL.
	if _, _, wasURL, _ := parseCallbackInput("https://x/?code=C&state=S"); !wasURL {
		t.Fatal("https input should be detected as a URL")
	}

	// Bare code → used verbatim, no state, wasURL=false (whitespace trimmed).
	code, state, wasURL, err = parseCallbackInput("  RAWCODE  ")
	if err != nil || wasURL || code != "RAWCODE" || state != "" {
		t.Fatalf("bare: got code=%q state=%q wasURL=%v err=%v", code, state, wasURL, err)
	}

	// Empty input → error.
	if _, _, _, err := parseCallbackInput("   "); err == nil {
		t.Fatal("empty input should error")
	}

	// A URL carrying an OAuth error still propagates that error.
	if _, _, _, err := parseCallbackInput("http://x/?error=access_denied"); !errors.Is(err, ErrAuthDenied) {
		t.Fatalf("access_denied URL should surface ErrAuthDenied, got %v", err)
	}
}

// TestClaimEmail covers claimEmail's branches directly (white-box), since
// Login's happy path can't be driven black-box (state is generated inside
// Login). Exercises: valid JWT with an email claim, a non-3-part token, and
// a bad-base64 payload segment.
func TestClaimEmail(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"user@example.com"}`))
	valid := "header." + payload + ".sig"
	if got := claimEmail(valid); got != "user@example.com" {
		t.Errorf("valid JWT: got %q, want user@example.com", got)
	}

	if got := claimEmail("only.two"); got != "" {
		t.Errorf("non-3-part token: got %q, want empty", got)
	}

	if got := claimEmail("header.not-valid-base64!!!.sig"); got != "" {
		t.Errorf("bad-base64 token: got %q, want empty", got)
	}

	badJSON := base64.RawURLEncoding.EncodeToString([]byte(`not json`))
	if got := claimEmail("header." + badJSON + ".sig"); got != "" {
		t.Errorf("bad-json payload: got %q, want empty", got)
	}
}
