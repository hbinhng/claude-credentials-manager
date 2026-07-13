package grokoauth

import (
	"encoding/base64"
	"testing"
)

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
