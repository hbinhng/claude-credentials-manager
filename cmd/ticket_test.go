package cmd

import (
	"strings"
	"testing"

	"github.com/hbinhng/claude-credentials-manager/internal/share"
)

func TestBuildTicket_HappyPath(t *testing.T) {
	got, err := buildTicket("https://abc.trycloudflare.com", "tok")
	if err != nil {
		t.Fatalf("buildTicket: %v", err)
	}
	dec, err := share.DecodeTicket(got)
	if err != nil {
		t.Fatalf("DecodeTicket(%q): %v", got, err)
	}
	want := share.Ticket{
		Scheme: "https",
		Host:   "abc.trycloudflare.com",
		Token:  "tok",
	}
	if dec != want {
		t.Errorf("decoded = %+v, want %+v", dec, want)
	}
}

func TestBuildTicket_HappyPathVariants(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		token    string
		want     share.Ticket
	}{
		{
			name:     "http with port",
			endpoint: "http://abc.com:8080",
			token:    "tok",
			want:     share.Ticket{Scheme: "http", Host: "abc.com:8080", Token: "tok"},
		},
		{
			name:     "ipv6 literal",
			endpoint: "https://[::1]:8080",
			token:    "tok",
			want:     share.Ticket{Scheme: "https", Host: "[::1]:8080", Token: "tok"},
		},
		{
			name:     "redundant default port preserved verbatim",
			endpoint: "https://abc.com:443",
			token:    "tok",
			want:     share.Ticket{Scheme: "https", Host: "abc.com:443", Token: "tok"},
		},
		{
			name:     "token with full unreserved alphabet",
			endpoint: "https://abc.com",
			token:    "AZaz09-._~",
			want:     share.Ticket{Scheme: "https", Host: "abc.com", Token: "AZaz09-._~"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildTicket(c.endpoint, c.token)
			if err != nil {
				t.Fatalf("buildTicket: %v", err)
			}
			dec, err := share.DecodeTicket(got)
			if err != nil {
				t.Fatalf("DecodeTicket: %v", err)
			}
			if dec != c.want {
				t.Errorf("decoded = %+v, want %+v", dec, c.want)
			}
		})
	}
}

func TestBuildTicket_Validation(t *testing.T) {
	cases := []struct {
		name      string
		endpoint  string
		token     string
		errSubstr string
	}{
		{"empty endpoint", "", "tok", "--from-endpoint is required"},
		{"non-http scheme", "ftp://abc.com", "tok", "scheme must be http or https"},
		{"missing host", "https://", "tok", "missing host"},
		{"trailing slash path", "https://abc.com/", "tok", "path not allowed"},
		{"path /v1", "https://abc.com/v1", "tok", "path not allowed"},
		{"query string", "https://abc.com?q=1", "tok", "query not allowed"},
		{"fragment", "https://abc.com#frag", "tok", "fragment not allowed"},
		{"userinfo", "https://user@abc.com", "tok", "userinfo not allowed"},
		{"empty token", "https://abc.com", "", "--from-access-token is required"},
		{"whitespace token", "https://abc.com", "  ", `byte 0 is " "`},
		{"token with @", "https://abc.com", "bad@token", `byte 3 is "@"`},
		{"token with :", "https://abc.com", "bad:token", `byte 3 is ":"`},
		{"token with /", "https://abc.com", "bad/token", `byte 3 is "/"`},
		{"token with +", "https://abc.com", "bad+token", `byte 3 is "+"`},
		{"token with =", "https://abc.com", "bad=token", `byte 3 is "="`},
		{"token with %", "https://abc.com", "ab%41", `byte 2 is "%"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildTicket(c.endpoint, c.token)
			if err == nil {
				t.Fatalf("buildTicket returned %q, want error containing %q", got, c.errSubstr)
			}
			if !strings.Contains(err.Error(), c.errSubstr) {
				t.Errorf("err = %v, want substring %q", err, c.errSubstr)
			}
		})
	}
}
