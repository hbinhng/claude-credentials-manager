package share

import "testing"

func TestTicketRoundTrip(t *testing.T) {
	orig := Ticket{
		Token: "deadbeefdeadbeefdeadbeefdeadbeef",
		Host:  "foo-bar-baz.trycloudflare.com",
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
		// base64 of "http://token@host" -- wrong scheme
		"aHR0cDovL3Rva2VuQGhvc3Q=",
		// base64 of "https://host" -- missing token
		"aHR0cHM6Ly9ob3N0",
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
	// 32 bytes -> 64 hex chars
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64", len(tok))
	}
}
