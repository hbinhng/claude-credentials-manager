package middleware

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

// decodeStream wraps a 2xx response body so it reads decompressed per
// Content-Encoding. ccm sends grok-shell's "Accept-Encoding: gzip, br,
// deflate", which disables Go's transparent decompression, and the
// Responses stream must be parsed (not relayed raw). Captures show grok
// streams SSE uncompressed; this is the defensive path. Unknown encodings
// pass through.
func decodeStream(h http.Header, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Encoding"))) {
	case "gzip":
		zr, err := gzip.NewReader(r)
		if err != nil {
			return errReaderOf(err)
		}
		return zr
	case "br":
		return brotli.NewReader(r)
	case "deflate":
		// HTTP "deflate" is nominally zlib (RFC 1950); some servers send
		// raw DEFLATE (RFC 1951). A zlib stream starts with a CMF/FLG pair
		// whose 16-bit value is a multiple of 31 with CM=8. FDICT (FLG bit
		// 0x20) is excluded: HTTP zlib never uses a preset dictionary, and
		// zlib.NewReader would consume the 4-byte dictionary id before
		// failing, leaving the raw flate fallback to start mid-stream.
		br := bufio.NewReader(r)
		if hdr, err := br.Peek(2); err == nil && hdr[0]&0x0f == 8 && hdr[1]&0x20 == 0 && (uint16(hdr[0])<<8|uint16(hdr[1]))%31 == 0 {
			if zr, err := zlib.NewReader(br); err == nil {
				return zr
			}
		}
		return flate.NewReader(br)
	default:
		return r
	}
}

type errReaderT struct{ err error }

func (e errReaderT) Read([]byte) (int, error) { return 0, e.err }

func errReaderOf(err error) io.Reader { return errReaderT{err} }
