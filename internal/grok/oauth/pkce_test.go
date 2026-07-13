package grokoauth

import (
	"net/url"
	"strings"
	"testing"
)

func TestGeneratePKCE_Shape(t *testing.T) {
	p, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE: %v", err)
	}
	if len(p.Verifier) < 43 || len(p.Verifier) > 128 {
		t.Fatalf("verifier length %d out of RFC 7636 range", len(p.Verifier))
	}
	if p.Challenge == "" || p.State == "" {
		t.Fatal("empty challenge/state")
	}
}

func TestBuildAuthorizeURL_Params(t *testing.T) {
	p := &PKCEParams{Verifier: "v", Challenge: "chal", State: "st"}
	raw := BuildAuthorizeURL(p, "http://127.0.0.1:56121/callback")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("code_challenge") != "chal" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("challenge params wrong: %v", q)
	}
	if q.Get("state") != "st" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("client_id") == "" {
		t.Error("client_id empty")
	}
	if !strings.HasPrefix(raw, AuthorizeURL) {
		t.Errorf("url %q does not start with AuthorizeURL %q", raw, AuthorizeURL)
	}
}
