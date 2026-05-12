package share

import (
	"testing"
)

func TestPassthroughEntryStateBasics(t *testing.T) {
	tk := Ticket{Scheme: "https", Host: "abc.trycloudflare.com", Token: "tok123"}
	pt := newPassthroughEntryState(tk)

	if got, _ := pt.Fresh(); got != "tok123" {
		t.Errorf("Fresh() = %q, want %q", got, "tok123")
	}
	if !pt.isPassthrough() {
		t.Errorf("isPassthrough() = false, want true")
	}
	if got := pt.upstreamURL(); got != "https://abc.trycloudflare.com" {
		t.Errorf("upstreamURL() = %q, want %q", got, "https://abc.trycloudflare.com")
	}
	if got := pt.credName(); got != "pt:abc.trycloudflare.com" {
		t.Errorf("credName() = %q, want %q", got, "pt:abc.trycloudflare.com")
	}
	if !pt.credExpiresAt().IsZero() {
		t.Errorf("credExpiresAt() should be zero, got %v", pt.credExpiresAt())
	}
	if pt.credPtr() != nil {
		t.Errorf("credPtr() should be nil, got %v", pt.credPtr())
	}
}

func TestPassthroughEntryStateIDStability(t *testing.T) {
	tk1 := Ticket{Scheme: "https", Host: "abc.trycloudflare.com", Token: "tok1"}
	tk2 := Ticket{Scheme: "https", Host: "ABC.TryCloudflare.COM", Token: "tok2"}
	tk3 := Ticket{Scheme: "https", Host: "def.trycloudflare.com", Token: "tok1"}

	id1 := newPassthroughEntryState(tk1).credID()
	id2 := newPassthroughEntryState(tk2).credID()
	id3 := newPassthroughEntryState(tk3).credID()

	if id1 != id2 {
		t.Errorf("same-host (case-insensitive) should produce same ID: %q vs %q", id1, id2)
	}
	if id1 == id3 {
		t.Errorf("different hosts should produce different IDs, both got %q", id1)
	}
	if len(id1) != len("pt:")+8 {
		t.Errorf("synthetic ID should be 'pt:' + 8 chars, got %q (len=%d)", id1, len(id1))
	}
}
