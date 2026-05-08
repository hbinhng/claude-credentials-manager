package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/credflow"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// stubHandshake builds a Handshake with only the exported fields set.
// The unexported codeVerifier/state fields are empty, which is fine
// because completeLoginFn is replaced by a seam in every claude test.
func stubHandshake(authorizeURL string) *credflow.Handshake {
	return &credflow.Handshake{AuthorizeURL: authorizeURL}
}

// setupLoginFakeHome creates a minimal ~/.ccm directory under a temp dir and
// sets HOME/USERPROFILE so store.Save writes into it.
func setupLoginFakeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".ccm"), 0700); err != nil {
		t.Fatalf("mkdir .ccm: %v", err)
	}
}

// --- parent loginCmd tests ---

func TestLogin_NoArg_ErrorsWithBothSubcommands(t *testing.T) {
	err := loginCmd.RunE(loginCmd, nil)
	if err == nil {
		t.Fatal("want error on bare ccm login")
	}
	if !strings.Contains(err.Error(), "ccm login claude") || !strings.Contains(err.Error(), "ccm login codex") {
		t.Fatalf("error must list both subcommands; got %v", err)
	}
}

// --- loginClaudeCmd tests ---

func TestLoginClaude_HappyPath(t *testing.T) {
	hs := stubHandshake("https://auth.example.com/auth")
	cred := &store.Credential{
		ID: "uuid-1234", Name: "alice@example.com",
		Provider: "claude",
	}
	stdinData := strings.NewReader("paste-code-here\n")
	cleanup := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(h *credflow.Handshake, code string) (*store.Credential, error) {
			if code != "paste-code-here" {
				t.Errorf("unexpected code: %q", code)
			}
			return cred, nil
		},
		stdinData,
	)
	defer cleanup()

	if err := loginClaudeCmd.RunE(loginClaudeCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginClaude_BeginLoginError_Propagates(t *testing.T) {
	cleanup := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return nil, errors.New("pkce boom") },
		func(*credflow.Handshake, string) (*store.Credential, error) { return nil, nil },
		strings.NewReader(""),
	)
	defer cleanup()

	err := loginClaudeCmd.RunE(loginClaudeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "pkce boom") {
		t.Fatalf("want propagated begin error; got %v", err)
	}
}

func TestLoginClaude_EmptyCode_Errors(t *testing.T) {
	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example.com/auth"}
	cleanup := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(*credflow.Handshake, string) (*store.Credential, error) { return nil, nil },
		strings.NewReader("   \n"),
	)
	defer cleanup()

	err := loginClaudeCmd.RunE(loginClaudeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "no code provided") {
		t.Fatalf("want 'no code provided'; got %v", err)
	}
}

func TestLoginClaude_EOFWithCode_Succeeds(t *testing.T) {
	// stdin has no trailing newline — reader.ReadString('\n') returns
	// io.EOF together with the bytes read; runLoginClaude must treat
	// that as a valid code.
	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example.com/auth"}
	cred := &store.Credential{ID: "u1", Name: "bob", Provider: "claude"}
	cleanup := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(*credflow.Handshake, string) (*store.Credential, error) { return cred, nil },
		strings.NewReader("code-no-newline"),
	)
	defer cleanup()

	if err := loginClaudeCmd.RunE(loginClaudeCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginClaude_StdinReadError_Propagates(t *testing.T) {
	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example.com/auth"}
	cleanup := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(*credflow.Handshake, string) (*store.Credential, error) { return nil, nil },
		&errReader{err: errors.New("disk error")},
	)
	defer cleanup()

	err := loginClaudeCmd.RunE(loginClaudeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "read code") {
		t.Fatalf("want 'read code' error; got %v", err)
	}
}

func TestLoginClaude_OpenStdinError_Propagates(t *testing.T) {
	// Drive the `open stdin: ...` error branch directly by replacing
	// the lower-level claudeLoginStdinFn (same package).
	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example.com/auth"}
	prevBegin := beginLoginFn
	prevStdin := claudeLoginStdinFn
	beginLoginFn = func() (*credflow.Handshake, error) { return hs, nil }
	claudeLoginStdinFn = func() (io.Reader, error) { return nil, errors.New("fd open failed") }
	defer func() {
		beginLoginFn = prevBegin
		claudeLoginStdinFn = prevStdin
	}()

	err := loginClaudeCmd.RunE(loginClaudeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "open stdin") {
		t.Fatalf("want 'open stdin' error; got %v", err)
	}
}

func TestLoginClaude_CompleteLoginError_Propagates(t *testing.T) {
	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example.com/auth"}
	cleanup := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(*credflow.Handshake, string) (*store.Credential, error) {
			return nil, errors.New("exchange failed")
		},
		strings.NewReader("mycode\n"),
	)
	defer cleanup()

	err := loginClaudeCmd.RunE(loginClaudeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "exchange failed") {
		t.Fatalf("want 'exchange failed'; got %v", err)
	}
}

func TestLoginClaude_FriendlyNameHint_WhenNameEqualsID(t *testing.T) {
	hs := &credflow.Handshake{AuthorizeURL: "https://auth.example.com/auth"}
	// When Name == ID the command prints a rename hint.
	cred := &store.Credential{ID: "12345678abcdefgh", Name: "12345678abcdefgh", Provider: "claude"}
	var out bytes.Buffer
	cleanup := SeamClaudeLogin(
		func() (*credflow.Handshake, error) { return hs, nil },
		func(*credflow.Handshake, string) (*store.Credential, error) { return cred, nil },
		strings.NewReader("mycode\n"),
	)
	defer cleanup()

	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := loginClaudeCmd.RunE(loginClaudeCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w.Close()
	os.Stdout = origStdout
	io.Copy(&out, r) //nolint:errcheck

	if !strings.Contains(out.String(), "ccm rename") {
		t.Fatalf("expected rename hint in output; got: %s", out.String())
	}
}

// errReader is an io.Reader that always returns an error after a
// few bytes so bufio.ReadString gets an error (not EOF) mid-read.
type errReader struct{ err error }

func (e *errReader) Read(p []byte) (int, error) { return 0, e.err }

// --- loginCodexCmd tests ---

func TestLoginCodex_HappyPath(t *testing.T) {
	setupLoginFakeHome(t)
	called := false
	cleanup := SeamCodexLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		called = true
		return &store.Credential{
			ID:              "01HZZZ-test-uuid",
			Name:            "u@x.com",
			Provider:        "codex",
			AuthMode:        "chatgpt",
			OpenAIAPIKey:    nil,
			Tokens:          &store.CodexTokens{IDToken: "i", AccessToken: "a", RefreshToken: "r", AccountID: "acct"},
			CreatedAt:       "t",
			LastRefreshedAt: "t",
			LastRefresh:     "t",
		}, nil
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	loginCodexCmd.SetOut(buf)
	if err := loginCodexCmd.RunE(loginCodexCmd, nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("codex login orchestrator not invoked")
	}
	if !strings.Contains(buf.String(), "Logged in as u@x.com") {
		t.Fatalf("unexpected output: %s", buf.String())
	}
}

func TestLoginCodex_LoginError_Propagates(t *testing.T) {
	setupLoginFakeHome(t)
	cleanup := SeamCodexLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		return nil, errors.New("user canceled")
	})
	defer cleanup()

	if err := loginCodexCmd.RunE(loginCodexCmd, nil); err == nil || !strings.Contains(err.Error(), "user canceled") {
		t.Fatalf("want propagated error; got %v", err)
	}
}

func TestLoginCodex_ShortIDPath(t *testing.T) {
	setupLoginFakeHome(t)
	cleanup := SeamCodexLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		return &store.Credential{
			ID:       "shortid",
			Name:     "u",
			Provider: "codex",
			AuthMode: "chatgpt",
			Tokens:   &store.CodexTokens{IDToken: "i", AccessToken: "a", RefreshToken: "r", AccountID: "acct"},
		}, nil
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	loginCodexCmd.SetOut(buf)
	if err := loginCodexCmd.RunE(loginCodexCmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "id: shortid") {
		t.Fatalf("expected raw id when len<=8: %s", buf.String())
	}
}

func TestLoginCodex_SaveError_Propagates(t *testing.T) {
	setupLoginFakeHome(t)
	// Make .ccm read-only so Save fails.
	home := os.Getenv("HOME")
	ccmDir := filepath.Join(home, ".ccm")
	if err := os.Chmod(ccmDir, 0500); err != nil {
		t.Skipf("cannot chmod .ccm: %v", err)
	}
	t.Cleanup(func() { os.Chmod(ccmDir, 0700) }) //nolint:errcheck

	cleanup := SeamCodexLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		return &store.Credential{
			ID:       "some-long-uuid-xxxx",
			Name:     "u",
			Provider: "codex",
			AuthMode: "chatgpt",
			Tokens:   &store.CodexTokens{IDToken: "i", AccessToken: "a", RefreshToken: "r", AccountID: "acct"},
		}, nil
	})
	defer cleanup()

	err := loginCodexCmd.RunE(loginCodexCmd, nil)
	if err == nil {
		t.Fatal("want save error, got nil")
	}
	if !strings.Contains(err.Error(), "save credential") {
		t.Fatalf("want 'save credential' in error; got %v", err)
	}
}
