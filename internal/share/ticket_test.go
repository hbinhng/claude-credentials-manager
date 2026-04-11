package share

import "testing"

func TestTicketRoundTripHTTPS(t *testing.T) {
	orig := Ticket{
		Scheme: "https",
		Token:  "deadbeefdeadbeefdeadbeefdeadbeef",
		Host:   "foo-bar-baz.trycloudflare.com",
	}
	got, err := DecodeTicket(orig.Encode())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	if got != orig {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, orig)
	}
}

// TestTicketRoundTripHTTP exercises the `ccm share --bind-host` mode
// where there is no Cloudflare tunnel, so the ticket carries a plain
// http:// base URL and a LAN-reachable host:port.
func TestTicketRoundTripHTTP(t *testing.T) {
	orig := Ticket{
		Scheme: "http",
		Token:  "abc123",
		Host:   "my-laptop.local:8080",
	}
	got, err := DecodeTicket(orig.Encode())
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	if got != orig {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestDecodeTicketRejectsGarbage(t *testing.T) {
	cases := []string{
		"",
		"not-base64!!!",
		// base64 of "ftp://token@host" -- neither http nor https
		"ZnRwOi8vdG9rZW5AaG9zdA==",
		// base64 of "https://host" -- missing token
		"aHR0cHM6Ly9ob3N0",
		// base64 of "http://token@" -- missing host
		"aHR0cDovL3Rva2VuQA==",
	}
	for _, in := range cases {
		if _, err := DecodeTicket(in); err == nil {
			t.Errorf("DecodeTicket(%q) succeeded, expected error", in)
		}
	}
}

func TestNewRandomTokenLength(t *testing.T) {
	tok, err := NewRandomToken()
	if err != nil {
		t.Fatalf("NewRandomToken: %v", err)
	}
	// 16 bytes -> 22 chars base64url (no padding). 128 bits of entropy
	// is plenty for an ephemeral share session.
	if len(tok) != 22 {
		t.Errorf("token length = %d, want 22", len(tok))
	}
}
