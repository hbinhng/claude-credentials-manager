package share

import (
	"net/http"
	"strings"
	"testing"
)

func TestMintViaID(t *testing.T) {
	a := mintViaID()
	b := mintViaID()
	if a == b {
		t.Errorf("mintViaID should be random; got duplicate %q", a)
	}
	if len(a) != 8 {
		t.Errorf("viaID length = %d, want 8", len(a))
	}
}

func TestAppendViaHeader(t *testing.T) {
	h := http.Header{}
	appendVia(h, "abc12345")
	if got := h.Get("Via"); got != "1.1 ccm-share/abc12345" {
		t.Errorf("Via = %q", got)
	}
	appendVia(h, "def67890")
	if got := h.Get("Via"); !strings.Contains(got, "abc12345") || !strings.Contains(got, "def67890") {
		t.Errorf("second append lost first: %q", got)
	}
}

func TestViaContainsLoop(t *testing.T) {
	h := http.Header{}
	if viaContains(h, "abc12345") {
		t.Errorf("empty Via should not contain")
	}
	appendVia(h, "other")
	if viaContains(h, "abc12345") {
		t.Errorf("Via with 'other' should not contain 'abc12345'")
	}
	appendVia(h, "abc12345")
	if !viaContains(h, "abc12345") {
		t.Errorf("Via should contain just-appended marker")
	}
}
