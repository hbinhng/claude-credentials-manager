package share

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

// mintViaID returns a fresh 8-character base64url loop-marker.
// Not secret — only used to detect request loops through ccm-share
// proxy chains via the HTTP Via header (RFC 7230 §5.7.1).
func mintViaID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		// coverage: unreachable — crypto/rand only errors on kernel RNG
		// failure. Defensive fallback: return a fixed sentinel so the
		// loop check never short-circuits silently.
		return "00000000"
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:8]
}

// appendVia appends "1.1 ccm-share/<id>" to the Via header chain,
// preserving any existing value so multi-hop chains are visible.
func appendVia(h http.Header, id string) {
	existing := h.Get("Via")
	marker := "1.1 ccm-share/" + id
	if existing == "" {
		h.Set("Via", marker)
		return
	}
	h.Set("Via", existing+", "+marker)
}

// viaContains reports whether the request's Via header already
// includes our viaID — i.e., the request has looped back to us.
func viaContains(h http.Header, id string) bool {
	v := h.Get("Via")
	if v == "" {
		return false
	}
	return strings.Contains(v, "ccm-share/"+id)
}
