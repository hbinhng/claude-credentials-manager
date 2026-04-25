package credflow

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// stubLoginSeams swaps every login-side seam at once and returns a
// restorer the test defers. Any nil arg leaves the original in place.
func stubLoginSeams(
	gen func() (*oauth.PKCEParams, error),
	exch func(code, state string, p *oauth.PKCEParams) (*oauth.TokenResponse, error),
	profile func(string) oauth.Profile,
	save func(*store.Credential) error,
) func() {
	origGen, origExch, origProf, origSave := loginGeneratePKCEFn, loginExchangeFn, loginFetchProfileFn, loginSaveFn
	if gen != nil {
		loginGeneratePKCEFn = gen
	}
	if exch != nil {
		loginExchangeFn = exch
	}
	if profile != nil {
		loginFetchProfileFn = profile
	}
	if save != nil {
		loginSaveFn = save
	}
	return func() {
		loginGeneratePKCEFn = origGen
		loginExchangeFn = origExch
		loginFetchProfileFn = origProf
		loginSaveFn = origSave
	}
}

func TestBeginLogin_PopulatesHandshake(t *testing.T) {
	hs, err := BeginLogin()
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if hs.ID == "" {
		t.Errorf("ID is empty")
	}
	// 16 random bytes → base64url no-padding = 22 chars
	if len(hs.ID) != 22 {
		t.Errorf("ID len = %d, want 22", len(hs.ID))
	}
	if _, err := base64.RawURLEncoding.DecodeString(hs.ID); err != nil {
		t.Errorf("ID is not base64url: %v", err)
	}
	if hs.codeVerifier == "" {
		t.Error("codeVerifier is empty")
	}
	if hs.state == "" {
		t.Error("state is empty")
	}
	if !strings.HasPrefix(hs.AuthorizeURL, oauth.AuthorizeURL+"?") {
		t.Errorf("AuthorizeURL = %q, want prefix %q", hs.AuthorizeURL, oauth.AuthorizeURL+"?")
	}
	parsed, err := url.Parse(hs.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse AuthorizeURL: %v", err)
	}
	if parsed.Query().Get("client_id") != oauth.ClientID {
		t.Errorf("client_id mismatch")
	}
}

func TestBeginLogin_PKCEError(t *testing.T) {
	defer stubLoginSeams(
		func() (*oauth.PKCEParams, error) { return nil, errors.New("rng fail") },
		nil, nil, nil,
	)()
	_, err := BeginLogin()
	if err == nil || !strings.Contains(err.Error(), "generate pkce") {
		t.Fatalf("err=%v, want 'generate pkce' wrap", err)
	}
}

func TestBeginLogin_IDError(t *testing.T) {
	origID := loginNewHandshakeIDFn
	loginNewHandshakeIDFn = func() (string, error) { return "", errors.New("entropy gone") }
	defer func() { loginNewHandshakeIDFn = origID }()
	_, err := BeginLogin()
	if err == nil || !strings.Contains(err.Error(), "new handshake id") {
		t.Fatalf("err=%v, want 'new handshake id' wrap", err)
	}
}

func TestNewHandshakeID_LengthAndAlphabet(t *testing.T) {
	id, err := newHandshakeID()
	if err != nil {
		t.Fatalf("newHandshakeID: %v", err)
	}
	if len(id) != 22 {
		t.Errorf("len=%d, want 22", len(id))
	}
	if _, err := base64.RawURLEncoding.DecodeString(id); err != nil {
		t.Errorf("not base64url: %v", err)
	}
}

// seedFakeHome ensures store.Save can write inside a tempdir.
func seedFakeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ccm"), 0700); err != nil {
		t.Fatalf("mkdir .ccm: %v", err)
	}
}

func TestCompleteLogin_HappyPath(t *testing.T) {
	seedFakeHome(t)
	defer stubLoginSeams(
		nil,
		func(code, state string, p *oauth.PKCEParams) (*oauth.TokenResponse, error) {
			if code != "the-code" {
				t.Errorf("exchange got code=%q, want the-code", code)
			}
			if state != "the-state" {
				t.Errorf("exchange got state=%q, want the-state", state)
			}
			if p.CodeVerifier != "the-verifier" {
				t.Errorf("exchange got verifier=%q, want the-verifier", p.CodeVerifier)
			}
			return &oauth.TokenResponse{
				AccessToken:  "ax",
				RefreshToken: "rx",
				ExpiresIn:    3600,
				Scope:        "user:inference user:profile",
			}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{Email: "alice@example.com", Tier: "Claude Pro"} },
		nil,
	)()

	hs := &Handshake{ID: "abc", AuthorizeURL: "u", codeVerifier: "the-verifier", state: "the-state"}
	cred, err := CompleteLogin(hs, "the-code")
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if cred.Name != "alice@example.com" {
		t.Errorf("Name = %q, want alice@example.com", cred.Name)
	}
	if cred.Subscription.Tier != "Claude Pro" {
		t.Errorf("Tier = %q", cred.Subscription.Tier)
	}
	if cred.ClaudeAiOauth.AccessToken != "ax" {
		t.Errorf("AccessToken = %q", cred.ClaudeAiOauth.AccessToken)
	}
	if cred.ClaudeAiOauth.RefreshToken != "rx" {
		t.Errorf("RefreshToken = %q", cred.ClaudeAiOauth.RefreshToken)
	}
	if len(cred.ClaudeAiOauth.Scopes) != 2 {
		t.Errorf("scopes len = %d, want 2", len(cred.ClaudeAiOauth.Scopes))
	}
	// Verify Save actually wrote the file (round-trip via Load).
	loaded, err := store.Load(cred.ID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if loaded.Name != "alice@example.com" {
		t.Errorf("loaded.Name = %q", loaded.Name)
	}
}

func TestCompleteLogin_EmptyScopeFallsBack(t *testing.T) {
	seedFakeHome(t)
	defer stubLoginSeams(
		nil,
		func(string, string, *oauth.PKCEParams) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: 1, Scope: "   "}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{Email: "x@y", Tier: "T"} },
		nil,
	)()
	hs := &Handshake{codeVerifier: "v", state: "s"}
	cred, err := CompleteLogin(hs, "code")
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	// Empty Scope should fall back to oauth.Scopes (length 4).
	if len(cred.ClaudeAiOauth.Scopes) != len(oauth.Scopes) {
		t.Errorf("scopes len = %d, want %d", len(cred.ClaudeAiOauth.Scopes), len(oauth.Scopes))
	}
}

func TestCompleteLogin_EmptyEmailFallsBackToID(t *testing.T) {
	seedFakeHome(t)
	defer stubLoginSeams(
		nil,
		func(string, string, *oauth.PKCEParams) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: 1, Scope: "user:inference"}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{} },
		nil,
	)()
	hs := &Handshake{codeVerifier: "v", state: "s"}
	cred, err := CompleteLogin(hs, "code")
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if cred.Name != cred.ID {
		t.Errorf("Name = %q, want = ID %q", cred.Name, cred.ID)
	}
}

func TestCompleteLogin_ExchangeError(t *testing.T) {
	seedFakeHome(t)
	defer stubLoginSeams(
		nil,
		func(string, string, *oauth.PKCEParams) (*oauth.TokenResponse, error) {
			return nil, errors.New("token request: connection reset")
		},
		nil, nil,
	)()
	hs := &Handshake{codeVerifier: "v", state: "s"}
	if _, err := CompleteLogin(hs, "code"); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err=%v, want propagated exchange error", err)
	}
}

func TestCompleteLogin_SaveError(t *testing.T) {
	seedFakeHome(t)
	defer stubLoginSeams(
		nil,
		func(string, string, *oauth.PKCEParams) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: 1, Scope: "user:inference"}, nil
		},
		func(string) oauth.Profile { return oauth.Profile{Email: "e@e"} },
		func(*store.Credential) error { return errors.New("disk full") },
	)()
	hs := &Handshake{codeVerifier: "v", state: "s"}
	if _, err := CompleteLogin(hs, "code"); err == nil || !strings.Contains(err.Error(), "save credentials") {
		t.Fatalf("err=%v, want 'save credentials' wrap", err)
	}
}
