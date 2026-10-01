package middleware

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestDecodeStream(t *testing.T) {
	const s = "event: x\ndata: {\"a\":1}\n\n"
	for name, tc := range map[string]struct {
		enc string
		raw []byte
	}{
		"identity":    {"", []byte(s)},
		"gzip":        {"gzip", gzipBytes(t, s)},
		"br":          {"br", brotliBytes(t, s)},
		"deflate":     {"deflate", zlibBytes(t, s)},
		"raw deflate": {"deflate", rawDeflateBytes(t, s)},
		"unknown":     {"zstd", []byte(s)},
	} {
		h := http.Header{}
		h.Set("Content-Encoding", tc.enc)
		got, err := io.ReadAll(decodeStream(h, bytes.NewReader(tc.raw)))
		if err != nil || string(got) != s {
			t.Errorf("%s: got %q err %v", name, got, err)
		}
	}
}

func TestDecodeStream_BadGzipErrorsOnRead(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Encoding", "gzip")
	if _, err := io.ReadAll(decodeStream(h, bytes.NewReader([]byte("not gzip")))); err == nil {
		t.Error("want error reading a corrupt gzip stream")
	}
}
