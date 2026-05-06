package usage

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"sync"
	"time"
)

// Sink consumes the response body bytes and produces zero or one
// Record on Finalize. Implementations: sseSink, jsonSink.
type Sink interface {
	io.Writer
	Finalize() *Record
}

// usageBlock mirrors the Anthropic /v1/messages "usage" object with
// pointer fields so we can distinguish "not present" (don't touch)
// from "present and zero" (overwrite to zero).
type usageBlock struct {
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
}

// ---- sseSink -----------------------------------------------------------

const sseLineCap = 32 * 1024

type sseSink struct {
	line     bytes.Buffer
	event    string
	in, out, cr, cw int64
	model    string
	overflow bool
	poisoned bool
	startTS  time.Time
}

func newSSESink() *sseSink {
	return &sseSink{startTS: time.Now().UTC()}
}

func (s *sseSink) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			if !s.overflow {
				s.processLine(s.line.Bytes())
			}
			s.line.Reset()
			s.overflow = false
			continue
		}
		if b == '\r' {
			continue
		}
		if s.line.Len() < sseLineCap {
			s.line.WriteByte(b)
		} else {
			s.overflow = true
		}
	}
	return len(p), nil
}

func (s *sseSink) processLine(b []byte) {
	switch {
	case bytes.HasPrefix(b, []byte("event: ")):
		s.event = string(b[7:])
		if s.event == "error" {
			s.poisoned = true
		}
	case bytes.HasPrefix(b, []byte("data: ")):
		if s.event == "message_start" || s.event == "message_delta" {
			s.parseUsageData(b[6:])
		}
	}
}

func (s *sseSink) parseUsageData(data []byte) {
	// message_start nests usage under "message". message_delta places
	// it at the top level. Try both shapes.
	var ms struct {
		Type    string `json:"type"`
		Message struct {
			Model string     `json:"model"`
			Usage usageBlock `json:"usage"`
		} `json:"message"`
		Usage usageBlock `json:"usage"`
	}
	if err := json.Unmarshal(data, &ms); err != nil {
		return
	}
	if ms.Message.Model != "" {
		s.model = ms.Message.Model
	}
	s.applyUsage(ms.Message.Usage)
	s.applyUsage(ms.Usage)
}

func (s *sseSink) applyUsage(u usageBlock) {
	if u.InputTokens != nil {
		s.in = *u.InputTokens
	}
	if u.OutputTokens != nil {
		s.out = *u.OutputTokens
	}
	if u.CacheReadInputTokens != nil {
		s.cr = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		s.cw = *u.CacheCreationInputTokens
	}
}

func (s *sseSink) Finalize() *Record {
	if s.poisoned {
		return nil
	}
	if s.in == 0 && s.out == 0 && s.cr == 0 && s.cw == 0 {
		return nil
	}
	return &Record{
		TS:     s.startTS,
		Model:  s.model,
		In:     s.in,
		Out:    s.out,
		CR:     s.cr,
		CW:     s.cw,
		Stream: true,
	}
}

// ---- jsonSink ----------------------------------------------------------

type jsonSink struct {
	buf     bytes.Buffer
	startTS time.Time
}

func newJSONSink() *jsonSink {
	return &jsonSink{startTS: time.Now().UTC()}
}

func (s *jsonSink) Write(p []byte) (int, error) { return s.buf.Write(p) }

// gzipMagic is the two-byte signature at the start of any gzip stream.
var gzipMagic = []byte{0x1f, 0x8b}

func (s *jsonSink) Finalize() *Record {
	body := s.buf.Bytes()
	// Anthropic sends gzip-compressed JSON for /v1/messages when
	// Accept-Encoding allows it (Go's default transport does). Detect
	// by magic bytes and decompress before unmarshaling. ReverseProxy
	// passes the original gzip bytes through to the client unchanged
	// — only our captured copy is decompressed.
	if bytes.HasPrefix(body, gzipMagic) {
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil
		}
		decoded, err := io.ReadAll(gr)
		gr.Close()
		if err != nil {
			return nil
		}
		body = decoded
	}
	var resp struct {
		Model string     `json:"model"`
		Usage usageBlock `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	in := derefOr(resp.Usage.InputTokens)
	out := derefOr(resp.Usage.OutputTokens)
	cr := derefOr(resp.Usage.CacheReadInputTokens)
	cw := derefOr(resp.Usage.CacheCreationInputTokens)
	if in == 0 && out == 0 && cr == 0 && cw == 0 {
		return nil
	}
	return &Record{
		TS:     s.startTS,
		Model:  resp.Model,
		In:     in,
		Out:    out,
		CR:     cr,
		CW:     cw,
		Stream: false,
	}
}

func derefOr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---- gzip wrapper ------------------------------------------------------

// gzipSink wraps another Sink and decompresses incoming gzip bytes
// before forwarding them. Used when an upstream response carries
// Content-Encoding: gzip — without this, sseSink sees raw gzip frames
// and never matches "event: " / "data: " prefixes.
//
// A pipe-backed goroutine drives gzip.Reader so that decompressed
// bytes arrive at the inner sink as soon as gzip emits them. On
// Finalize, the pipe is closed and we wait for the goroutine to
// drain before consulting the inner sink.
type gzipSink struct {
	inner  Sink
	pw     *io.PipeWriter
	done   chan struct{}
	closed bool
}

// NewGzipSink wraps another Sink so that gzip-compressed bytes
// written to it are decompressed before being forwarded. Used for
// SSE responses with Content-Encoding: gzip — without this wrapper,
// sseSink sees raw deflate frames and never matches event/data
// prefixes.
func NewGzipSink(inner Sink) Sink { return newGzipSink(inner) }

func newGzipSink(inner Sink) *gzipSink {
	pr, pw := io.Pipe()
	g := &gzipSink{inner: inner, pw: pw, done: make(chan struct{})}
	go g.run(pr)
	return g
}

func (g *gzipSink) run(pr *io.PipeReader) {
	defer close(g.done)
	defer pr.Close()
	gr, err := gzip.NewReader(pr)
	if err != nil {
		// Malformed gzip — drain pipe so the writer side doesn't
		// block, but emit nothing.
		_, _ = io.Copy(io.Discard, pr)
		return
	}
	defer gr.Close()
	buf := make([]byte, 8*1024)
	for {
		n, err := gr.Read(buf)
		if n > 0 {
			_, _ = g.inner.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (g *gzipSink) Write(p []byte) (int, error) {
	if g.closed {
		return len(p), nil
	}
	// Pipe writes block until the goroutine has consumed them. gzip
	// throughput is very high (>>100 MB/s), so this is effectively
	// memcpy speed for our SSE chunks.
	return g.pw.Write(p)
}

func (g *gzipSink) Finalize() *Record {
	if !g.closed {
		g.closed = true
		_ = g.pw.Close()
	}
	<-g.done
	return g.inner.Finalize()
}

// ---- dispatch ----------------------------------------------------------

// NewSink returns the appropriate Sink based on the Content-Type header.
// Returns nil if Content-Type is malformed (caller should skip the tee).
//
// sessionID is captured for downstream Append; the dispatcher itself
// does not validate it (TeeBody guards path access).
//
// ts is the response start timestamp; it becomes Record.TS on Finalize.
func NewSink(sessionID string, header http.Header, ts time.Time) Sink {
	ct := header.Get("Content-Type")
	if ct == "" {
		s := newJSONSink()
		s.startTS = ts
		return s
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil
	}
	if mediaType == "text/event-stream" {
		s := newSSESink()
		s.startTS = ts
		return s
	}
	s := newJSONSink()
	s.startTS = ts
	return s
}

// ---- TeeBody -----------------------------------------------------------

// debugLog writes a line to stderr only when CCM_DEBUG_USAGE is set
// in the environment. Default is silent — usage tracking is
// observability and must not contaminate proxy stderr in production.
func debugLog(format string, args ...interface{}) {
	if os.Getenv("CCM_DEBUG_USAGE") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "ccm usage: "+format+"\n", args...)
}

// TeeBody wraps an http.Response.Body with a tee that mirrors bytes
// into sink and finalizes (writing one NDJSON line) on the first
// terminal Read return or on Close, whichever comes first. Idempotent
// via sync.Once.
//
// sessionID must already be validated by the caller; if invalid we
// silently drop on Finalize.
func TeeBody(body io.ReadCloser, sink Sink, sessionID string) io.ReadCloser {
	return &teeBody{
		body:      body,
		sink:      sink,
		sessionID: sessionID,
	}
}

type teeBody struct {
	body      io.ReadCloser
	sink      Sink
	sessionID string
	once      sync.Once
	closeOnce sync.Once
	closeErr  error
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.body.Read(p)
	if n > 0 {
		t.safeWrite(p[:n])
	}
	if err != nil {
		t.finalize()
	}
	return n, err
}

// Close is idempotent. ReverseProxy.copyResponse closes the body
// after copy, and we may also be called by the client; some
// io.ReadCloser implementations are not safe to Close twice.
func (t *teeBody) Close() error {
	t.finalize()
	t.closeOnce.Do(func() {
		t.closeErr = t.body.Close()
	})
	return t.closeErr
}

func (t *teeBody) safeWrite(p []byte) {
	defer func() {
		if r := recover(); r != nil {
			debugLog("sink panic recovered: %v", r)
		}
	}()
	if t.sink == nil {
		return
	}
	_, _ = t.sink.Write(p)
}

func (t *teeBody) finalize() {
	t.once.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				debugLog("finalize panic recovered: %v", r)
			}
		}()
		if t.sink == nil {
			return
		}
		rec := t.sink.Finalize()
		if rec == nil {
			return
		}
		if !IsValidSessionID(t.sessionID) {
			return
		}
		if err := Append(t.sessionID, *rec); err != nil {
			debugLog("append failed: %v", err)
		}
	})
}
