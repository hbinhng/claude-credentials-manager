package grokoauth_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	grokoauth "github.com/hbinhng/claude-credentials-manager/internal/grok/oauth"
)

func TestParseCallbackURL_Errors(t *testing.T) {
	if _, _, err := grokoauth.ExportedParseCallbackURL(""); err == nil {
		t.Error("empty URL should error")
	}
	if _, _, err := grokoauth.ExportedParseCallbackURL("http://x/?error=access_denied"); err == nil {
		t.Error("access_denied should error")
	}
	code, state, err := grokoauth.ExportedParseCallbackURL("http://x/?code=C&state=S")
	if err != nil || code != "C" || state != "S" {
		t.Fatalf("got %q/%q err=%v", code, state, err)
	}
}

func TestLogin_StateMismatch(t *testing.T) {
	in := strings.NewReader("http://127.0.0.1:56121/callback?code=C&state=WRONG\n")
	_, err := grokoauth.Login(context.Background(), new(bytes.Buffer), in)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("want state mismatch, got %v", err)
	}
}

func TestLogin_EmptyStdin(t *testing.T) {
	_, err := grokoauth.Login(context.Background(), new(bytes.Buffer), strings.NewReader(""))
	if err == nil {
		t.Fatal("want error on empty stdin")
	}
}
