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

// A raw DEFLATE stream can start with bytes that pass the zlib CMF/FLG
// check but have FDICT set: 0x78 0x20 is a non-final stored block (BTYPE=00)
// with LEN 0x0020. zlib.NewReader would consume the 4-byte dictionary id
// before failing with ErrDictionary, so the raw flate fallback would start
// mid-stream; the zlib attempt must be skipped for FDICT headers.
func TestDecodeStream_RawDeflateLookingLikeZlibWithFDICT(t *testing.T) {
	payload := bytes.Repeat([]byte("ab"), 16) // 32 bytes == LEN
	raw := []byte{0x78, 0x20, 0x00, 0xdf, 0xff}
	raw = append(raw, payload...)
	raw = append(raw, 0x01, 0x00, 0x00, 0xff, 0xff) // final empty stored block
	h := http.Header{}
	h.Set("Content-Encoding", "deflate")
	got, err := io.ReadAll(decodeStream(h, bytes.NewReader(raw)))
	if err != nil || !bytes.Equal(got, payload) {
		t.Errorf("got %q err %v, want %q", got, err, payload)
	}
}
