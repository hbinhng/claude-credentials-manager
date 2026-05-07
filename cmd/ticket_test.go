package cmd

import (
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
