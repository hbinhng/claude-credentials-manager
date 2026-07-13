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
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

func TestLoginGrok_SavesCredential(t *testing.T) {
	setupFakeHome(t)
	restore := SeamGrokLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		c := &store.Credential{ID: "gid-123456789", Name: "me@x.ai", Provider: "grok", GrokTokens: &store.GrokTokens{AccessToken: "a", RefreshToken: "r"}}
		c.SetTokens("a", "r", time.Now().Add(time.Hour).UnixMilli())
		return c, nil
	})
	defer restore()

	var out bytes.Buffer
	loginGrokCmd.SetOut(&out)
	if err := loginGrokCmd.RunE(loginGrokCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if _, err := store.Load("gid-123456789"); err != nil {
		t.Fatalf("credential not saved: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("me@x.ai")) {
		t.Errorf("output missing name: %s", out.String())
	}
}

func TestLoginGrok_LoginError_Propagates(t *testing.T) {
	setupFakeHome(t)
	restore := SeamGrokLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		return nil, errors.New("user canceled")
	})
	defer restore()

	if err := loginGrokCmd.RunE(loginGrokCmd, nil); err == nil || !strings.Contains(err.Error(), "user canceled") {
		t.Fatalf("want propagated error; got %v", err)
	}
}

func TestLoginGrok_ShortIDPath(t *testing.T) {
	setupFakeHome(t)
	restore := SeamGrokLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		c := &store.Credential{ID: "shortid", Name: "u", Provider: "grok"}
		c.SetTokens("a", "r", time.Now().Add(time.Hour).UnixMilli())
		return c, nil
	})
	defer restore()

	var out bytes.Buffer
	loginGrokCmd.SetOut(&out)
	if err := loginGrokCmd.RunE(loginGrokCmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "id: shortid") {
		t.Fatalf("expected raw id when len<=8: %s", out.String())
	}
}

func TestLoginGrok_SaveError_Propagates(t *testing.T) {
	home := setupFakeHome(t)
	// Make .ccm read-only so Save fails.
	ccmDir := filepath.Join(home, ".ccm")
	if err := os.Chmod(ccmDir, 0500); err != nil {
		t.Skipf("cannot chmod .ccm: %v", err)
	}
	t.Cleanup(func() { os.Chmod(ccmDir, 0700) }) //nolint:errcheck

	restore := SeamGrokLogin(func(ctx context.Context, w io.Writer, r io.Reader) (*store.Credential, error) {
		c := &store.Credential{ID: "some-long-uuid-xxxx", Name: "u", Provider: "grok"}
		c.SetTokens("a", "r", time.Now().Add(time.Hour).UnixMilli())
		return c, nil
	})
	defer restore()

	err := loginGrokCmd.RunE(loginGrokCmd, nil)
	if err == nil {
		t.Fatal("want save error, got nil")
	}
	if !strings.Contains(err.Error(), "save credential") {
		t.Fatalf("want save credential error; got %v", err)
	}
}
